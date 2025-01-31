/*
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"github.com/google/uuid"
	govalkey "github.com/redis/go-redis/v9"
	"golang.org/x/mod/semver"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	prometheusv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/sap/component-operator-runtime/pkg/component"
	"github.com/sap/go-generics/slices"
	operatorv1alpha1 "github.com/sap/valkey-operator/api/v1alpha1"
	"github.com/sap/valkey-operator/pkg/operator"
)

var enabled bool
var kubeconfig string
var image string
var hostname string
var kind string

func init() {
	var err error

	enabled = os.Getenv("E2E_ENABLED") == "true"
	kubeconfig = os.Getenv("E2E_KUBECONFIG")
	image = os.Getenv("E2E_IMAGE")

	hostname = os.Getenv("E2E_HOSTNAME")
	if hostname == "" {
		hostname, err = os.Hostname()
		if err != nil {
			panic(err)
		}
	}
	hostname = strings.ToLower(hostname)

	kind = os.Getenv("E2E_KIND")
	if kind == "" {
		kind, err = exec.LookPath("kind")
		if err != nil {
			kind = ""
		}
	}
}

func TestOperator(t *testing.T) {
	if !enabled {
		t.Skip("Skipped because end-to-end tests are not enabled")
	}
	RegisterFailHandler(Fail)
	RunSpecs(t, "Operator")
}

var kindEnv string
var testEnv *envtest.Environment
var cfg *rest.Config
var cli client.Client
var discoveryCli discovery.DiscoveryInterface
var ctx context.Context
var cancel context.CancelFunc
var threads sync.WaitGroup
var tmpdir string

var _ = BeforeSuite(func() {
	var err error

	if kubeconfig == "" && kind == "" {
		Fail("No kubeconfig provided, and no kind executable was provided or found in the path")
	}

	By("initializing")
	log.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())
	tmpdir, err = os.MkdirTemp("", "")
	Expect(err).NotTo(HaveOccurred())

	if kubeconfig == "" {
		By("bootstrapping kind cluster")
		kindEnv = fmt.Sprintf("kind-%s", filepath.Base(tmpdir))
		kubeconfig = fmt.Sprintf("%s/kubeconfig", tmpdir)
		err := createKindCluster(ctx, kind, kindEnv, kubeconfig)
		Expect(err).NotTo(HaveOccurred())
	}

	By("fetching rest config")
	cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}, nil).ClientConfig()
	Expect(err).NotTo(HaveOccurred())

	By("populating scheme")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(apiregistrationv1.AddToScheme(scheme))
	utilruntime.Must(certmanagerv1.AddToScheme(scheme))
	utilruntime.Must(prometheusv1.AddToScheme(scheme))
	operator.InitScheme(scheme)

	By("initializing client")
	cli, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	discoveryCli, err = discovery.NewDiscoveryClientForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())

	By("validating cluster")
	kubernetesVersion, err := discoveryCli.ServerVersion()
	Expect(err).NotTo(HaveOccurred())
	Expect(semver.Compare(kubernetesVersion.GitVersion, "v1.27.0") >= 0).To(BeTrue())
	apiGroups, err := discoveryCli.ServerGroups()
	Expect(err).NotTo(HaveOccurred())
	Expect(slices.Collect(apiGroups.Groups, func(g metav1.APIGroup) string { return g.Name })).To(ContainElement("cert-manager.io"))

	if image == "" {
		By("bootstrapping test environment")
		testEnv = &envtest.Environment{
			UseExistingCluster: &[]bool{true}[0],
			Config:             cfg,
			CRDDirectoryPaths:  []string{"../../crds"},
			WebhookInstallOptions: envtest.WebhookInstallOptions{
				LocalServingHost: hostname,
				ValidatingWebhooks: []*admissionv1.ValidatingWebhookConfiguration{
					buildValidatingWebhookConfiguration(),
				},
			},
		}
		_, err = testEnv.Start()
		Expect(err).NotTo(HaveOccurred())
		webhookInstallOptions := &testEnv.WebhookInstallOptions

		By("creating manager")
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: scheme,
			Client: client.Options{
				Cache: &client.CacheOptions{
					DisableFor: append(operator.GetUncacheableTypes(), &apiextensionsv1.CustomResourceDefinition{}, &apiregistrationv1.APIService{}),
				},
			},
			WebhookServer: webhook.NewServer(webhook.Options{
				Host:    webhookInstallOptions.LocalServingHost,
				Port:    webhookInstallOptions.LocalServingPort,
				CertDir: webhookInstallOptions.LocalServingCertDir,
			}),
			Metrics: metricsserver.Options{
				BindAddress: "0",
			},
			HealthProbeBindAddress: "0",
		})
		Expect(err).NotTo(HaveOccurred())

		err = operator.Setup(mgr)
		Expect(err).NotTo(HaveOccurred())

		By("starting manager")
		threads.Add(1)
		go func() {
			defer threads.Done()
			defer GinkgoRecover()
			err := mgr.Start(ctx)
			Expect(err).NotTo(HaveOccurred())
		}()

		By("waiting for operator to become ready")
		Eventually(func() error { return mgr.GetWebhookServer().StartedChecker()(nil) }, "10s", "100ms").Should(Succeed())
	} else {
		By("bootstrapping test environment")
		testEnv = &envtest.Environment{
			UseExistingCluster: &[]bool{true}[0],
			CRDDirectoryPaths:  []string{"../../crds"},
		}
		_, err = testEnv.Start()
		Expect(err).NotTo(HaveOccurred())
		// TODO: deploy image, rbac, service, webhook
		panic("not yet implemented")
	}
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	threads.Wait()
	if testEnv != nil {
		err := testEnv.Stop()
		Expect(err).NotTo(HaveOccurred())
	}
	if kindEnv != "" {
		err := deleteKindCluster(kind, kindEnv)
		Expect(err).NotTo(HaveOccurred())
	}
	err := os.RemoveAll(tmpdir)
	Expect(err).NotTo(HaveOccurred())
})

var _ = Describe("Deploy Valkey", func() {
	var namespace string

	BeforeEach(func() {
		namespace = createNamespace()
	})

	AfterEach(func() {
		// add some delay to avoid situations where the operator creates late events, which
		// would fail if the namespace is already in terminating state
		time.Sleep(5 * time.Second)
		deleteNamespace(namespace)
	})

	It("should deploy Valkey with one primary and zero read replicas", func() {
		valkey := &operatorv1alpha1.Valkey{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    namespace,
				GenerateName: "test-",
			},
		}
		defer deleteValkey(valkey, true, "60s")
		createValkey(valkey, true, "300s")
		doSomethingWithValkey(valkey)
	})

	It("should deploy Valkey with sentinel (three nodes), with metrics, TLS, persistence enabled", func() {
		valkey := &operatorv1alpha1.Valkey{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    namespace,
				GenerateName: "test-",
			},
			Spec: operatorv1alpha1.ValkeySpec{
				Replicas: 3,
				Sentinel: &operatorv1alpha1.SentinelProperties{
					Enabled: true,
				},
				Metrics: &operatorv1alpha1.MetricsProperties{
					Enabled: true,
				},
				TLS: &operatorv1alpha1.TLSProperties{
					Enabled: true,
				},
				Persistence: &operatorv1alpha1.PersistenceProperties{
					Enabled: true,
				},
			},
		}
		defer deleteValkey(valkey, true, "60s")
		createValkey(valkey, true, "300s")
		doSomethingWithValkey(valkey)
	})

	It("should deploy Valkey with one primary and one read replica, with TLS enabled, provided by cert-manager (self-signed)", func() {
		valkey := &operatorv1alpha1.Valkey{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    namespace,
				GenerateName: "test-",
			},
			Spec: operatorv1alpha1.ValkeySpec{
				Replicas: 2,
				TLS: &operatorv1alpha1.TLSProperties{
					Enabled:     true,
					CertManager: &operatorv1alpha1.CertManagerProperties{},
				},
			},
		}
		defer deleteValkey(valkey, true, "60s")
		createValkey(valkey, true, "300s")
		doSomethingWithValkey(valkey)
	})

	It("should deploy Valkey with sentinel (one node), with TLS enabled, provided by cert-manager (existing issuer)", func() {
		createIssuer(namespace, "test")
		valkey := &operatorv1alpha1.Valkey{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    namespace,
				GenerateName: "test-",
			},
			Spec: operatorv1alpha1.ValkeySpec{
				Replicas: 1,
				Sentinel: &operatorv1alpha1.SentinelProperties{
					Enabled: true,
				},
				TLS: &operatorv1alpha1.TLSProperties{
					Enabled: true,
					CertManager: &operatorv1alpha1.CertManagerProperties{
						Issuer: &operatorv1alpha1.ObjectReference{Name: "test"},
					},
				},
			},
		}
		defer deleteValkey(valkey, true, "60s")
		createValkey(valkey, true, "300s")
		doSomethingWithValkey(valkey)
	})

	It("should deploy Valkey without sentinel, 1 node, with TLS disabled with metrics, service monitor and prometheus rule enabled", func() {
		var duration prometheusv1.Duration = "5m"
		valkey := &operatorv1alpha1.Valkey{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    namespace,
				GenerateName: "test-",
			},
			Spec: operatorv1alpha1.ValkeySpec{
				Replicas: 1,
				Sentinel: &operatorv1alpha1.SentinelProperties{
					Enabled: false,
				},
				TLS: &operatorv1alpha1.TLSProperties{
					Enabled: false,
				},
				Metrics: &operatorv1alpha1.MetricsProperties{
					Enabled: true,
					ServiceMonitor: &operatorv1alpha1.MetricsServiceMonitorProperties{
						Enabled:       true,
						Interval:      "30s",
						ScrapeTimeout: "10s",
						Relabellings: []prometheusv1.RelabelConfig{
							{SourceLabels: []prometheusv1.LabelName{"__meta_kubernetes_namespace"}, TargetLabel: "namespace"},
							{SourceLabels: []prometheusv1.LabelName{"__meta_kubernetes_pod_name"}, TargetLabel: "pod"},
						},
						MetricRelabellings: []prometheusv1.RelabelConfig{
							{SourceLabels: []prometheusv1.LabelName{"__name__"}, TargetLabel: "metric"},
							{SourceLabels: []prometheusv1.LabelName{"__meta_kubernetes_pod_name"}, TargetLabel: "pod"},
						},
						HonorLabels: true,
						AdditionalLabels: map[string]string{
							"app": "valkey",
						},
						PodTargetLabels: []string{"app"},
					},
					PrometheusRule: &operatorv1alpha1.MetricsPrometheusRuleProperties{
						Enabled: true,
						AdditionalLabels: map[string]string{
							"app": "valkey",
						},
						Rules: []prometheusv1.Rule{
							{
								Record: "valkey:metrics:exporter:scrape_duration_seconds:avg",
								Expr:   intstr.FromString("avg(valkey:metrics:exporter:scrape_duration_seconds) by (namespace, pod)"),
							},
							{
								Alert: "ValkeyExporterDown",
								Expr:  intstr.FromString("up{job=\"valkey-exporter\"} == 0"),
								For:   &duration,
								Labels: map[string]string{
									"severity": "critical",
								},
								Annotations: map[string]string{
									"summary": "Valkey exporter is down",
								},
							},
						},
					},
				},
			},
		}
		defer deleteValkey(valkey, true, "60s")
		createValkey(valkey, true, "300s")
		doSomethingWithValkey(valkey)
		checkServiceForMetrics(valkey)
		checkServiceMonitor(valkey)
		checkPrometheusRule(valkey)
	})

})

func createNamespace() string {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-"}}
	err := cli.Create(ctx, namespace)
	Expect(err).NotTo(HaveOccurred())
	return namespace.Name
}

func deleteNamespace(name string) {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := client.IgnoreNotFound(cli.Delete(ctx, namespace))
	Expect(err).NotTo(HaveOccurred())
}

func createIssuer(namespace string, name string) {
	caKey, caCert, err := generateCertificateAuthority()
	Expect(err).NotTo(HaveOccurred())

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      fmt.Sprintf("%s-ca", name),
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.key": caKey,
			"tls.crt": caCert,
		},
	}
	err = cli.Create(ctx, secret)
	Expect(err).NotTo(HaveOccurred())

	issuer := &certmanagerv1.Issuer{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: certmanagerv1.IssuerSpec{
			IssuerConfig: certmanagerv1.IssuerConfig{
				CA: &certmanagerv1.CAIssuer{
					SecretName: secret.Name,
				},
			},
		},
	}
	err = cli.Create(ctx, issuer)
	Expect(err).NotTo(HaveOccurred())
}

func createValkey(valkey *operatorv1alpha1.Valkey, wait bool, timeout string) {
	err := cli.Create(ctx, valkey)
	Expect(err).NotTo(HaveOccurred())
	if !wait {
		return
	}
	Eventually(func() error {
		if err := cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: valkey.Name}, valkey); err != nil {
			return err
		}
		if valkey.Status.ObservedGeneration != valkey.Generation || valkey.Status.State != component.StateReady {
			return fmt.Errorf("not ready - try again")
		}
		return nil
	}, timeout, "1s").Should(Succeed())
}

func deleteValkey(valkey *operatorv1alpha1.Valkey, wait bool, timeout string) {
	err := cli.Delete(ctx, valkey)
	Expect(err).NotTo(HaveOccurred())
	if !wait {
		return
	}
	Eventually(func() error {
		if err := cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: valkey.Name}, valkey); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		return fmt.Errorf("still existing - try again")
	}, timeout, "1s").Should(Succeed())
}

func doSomethingWithValkey(valkey *operatorv1alpha1.Valkey) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	bindingSecret := &corev1.Secret{}
	err := cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: fmt.Sprintf("valkey-%s-binding", valkey.Name)}, bindingSecret)
	Expect(err).NotTo(HaveOccurred())

	binding := bindingSecret.Data
	password := string(binding["password"])
	var tlsConfig *tls.Config
	if string(binding["tlsEnabled"]) == "true" {
		certPool := x509.NewCertPool()
		certsAdded := certPool.AppendCertsFromPEM(binding["caData"])
		Expect(certsAdded).To(BeTrue())
		tlsConfig = &tls.Config{
			RootCAs: certPool,
		}
	}

	if string(binding["sentinelEnabled"]) == "true" {
		valkeyNodeMap := make(ValkeyNodeMap)
		err = addServiceToValkeyNodeMap(ctx, cli, valkey.Namespace, fmt.Sprintf("valkey-%s", valkey.Name), valkeyNodeMap)
		Expect(err).NotTo(HaveOccurred())
		err = addServiceToValkeyNodeMap(ctx, cli, valkey.Namespace, fmt.Sprintf("valkey-%s-headless", valkey.Name), valkeyNodeMap)
		Expect(err).NotTo(HaveOccurred())
		for _, node := range valkeyNodeMap {
			if node.LocalPort == 0 {
				node.LocalPort, err = startPortForwarding(ctx, cfg, valkey.Namespace, node.PodName, node.Port)
				Expect(err).NotTo(HaveOccurred())
			}
		}

		sentinelNode, ok := valkeyNodeMap[fmt.Sprintf("%s:%s", binding["sentinelHost"], binding["sentinelPort"])]
		Expect(ok).To(BeTrue())
		sentinelClient := govalkey.NewSentinelClient(&govalkey.Options{
			Addr:      fmt.Sprintf("localhost:%d", sentinelNode.LocalPort),
			Password:  password,
			TLSConfig: tlsConfig,
		})

		primaryAddress, err := sentinelClient.GetMasterAddrByName(ctx, string(binding["primaryName"])).Result()
		Expect(err).NotTo(HaveOccurred())

		primaryNode, ok := valkeyNodeMap[fmt.Sprintf("%s:%s", primaryAddress[0], primaryAddress[1])]
		Expect(ok).To(BeTrue())
		primaryClient := govalkey.NewClient(&govalkey.Options{
			Addr:      fmt.Sprintf("localhost:%d", primaryNode.LocalPort),
			Password:  password,
			TLSConfig: tlsConfig,
			DB:        0,
		})

		value := uuid.New().String()
		err = primaryClient.Set(ctx, "some-key", value, 0).Err()
		Expect(err).NotTo(HaveOccurred())

		val, err := primaryClient.Get(ctx, "some-key").Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(val).To(Equal(value))

		// TODO: it may happen that readerClient uses the primary; should we improve this ?
		readerNode, ok := valkeyNodeMap[fmt.Sprintf("%s:%s", binding["host"], binding["port"])]
		Expect(ok).To(BeTrue())
		readerClient := govalkey.NewClient(&govalkey.Options{
			Addr:      fmt.Sprintf("localhost:%d", readerNode.LocalPort),
			Password:  password,
			TLSConfig: tlsConfig,
			DB:        0,
		})

		val, err = readerClient.Get(ctx, "some-key").Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(val).To(Equal(value))
	} else {
		valkeyNodeMap := make(ValkeyNodeMap)
		err = addServiceToValkeyNodeMap(ctx, cli, valkey.Namespace, fmt.Sprintf("valkey-%s-primary", valkey.Name), valkeyNodeMap)
		Expect(err).NotTo(HaveOccurred())
		err = addServiceToValkeyNodeMap(ctx, cli, valkey.Namespace, fmt.Sprintf("valkey-%s-replicas", valkey.Name), valkeyNodeMap)
		Expect(err).NotTo(HaveOccurred())
		err = addServiceToValkeyNodeMap(ctx, cli, valkey.Namespace, fmt.Sprintf("valkey-%s-headless", valkey.Name), valkeyNodeMap)
		Expect(err).NotTo(HaveOccurred())
		for _, node := range valkeyNodeMap {
			if node.LocalPort == 0 {
				node.LocalPort, err = startPortForwarding(ctx, cfg, valkey.Namespace, node.PodName, node.Port)
				Expect(err).NotTo(HaveOccurred())
			}
		}

		primaryNode, ok := valkeyNodeMap[fmt.Sprintf("%s:%s", binding["primaryHost"], binding["primaryPort"])]
		Expect(ok).To(BeTrue())
		primaryClient := govalkey.NewClient(&govalkey.Options{
			Addr:      fmt.Sprintf("localhost:%d", primaryNode.LocalPort),
			Password:  password,
			TLSConfig: tlsConfig,
			DB:        0,
		})

		value := uuid.New().String()
		err = primaryClient.Set(ctx, "some-key", value, 0).Err()
		Expect(err).NotTo(HaveOccurred())

		val, err := primaryClient.Get(ctx, "some-key").Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(val).To(Equal(value))

		if valkey.Spec.Replicas > 1 {
			replicaNode, ok := valkeyNodeMap[fmt.Sprintf("%s:%s", binding["replicaHost"], binding["replicaPort"])]
			Expect(ok).To(BeTrue())
			replicaClient := govalkey.NewClient(&govalkey.Options{
				Addr:      fmt.Sprintf("localhost:%d", replicaNode.LocalPort),
				Password:  password,
				TLSConfig: tlsConfig,
				DB:        0,
			})

			val, err := replicaClient.Get(ctx, "some-key").Result()
			Expect(err).NotTo(HaveOccurred())
			Expect(val).To(Equal(value))
		}
	}
}

func checkServiceForMetrics(valkey *operatorv1alpha1.Valkey) {
	service := &corev1.Service{}
	serviceName := fmt.Sprintf("valkey-%s-metrics", valkey.Name)
	err := cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: serviceName}, service)
	Expect(err).NotTo(HaveOccurred())
}

func checkServiceMonitor(valkey *operatorv1alpha1.Valkey) {
	serviceMonitor := &prometheusv1.ServiceMonitor{}
	serviceMonitorName := fmt.Sprintf("valkey-%s", valkey.Name)
	err := cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: serviceMonitorName}, serviceMonitor)
	Expect(err).NotTo(HaveOccurred())

	Expect(serviceMonitor.Spec.Endpoints[len(serviceMonitor.Spec.Endpoints)-1].Interval).To(Equal(valkey.Spec.Metrics.ServiceMonitor.Interval))
	Expect(serviceMonitor.Spec.Endpoints[len(serviceMonitor.Spec.Endpoints)-1].ScrapeTimeout).To(Equal(valkey.Spec.Metrics.ServiceMonitor.ScrapeTimeout))
	Expect(serviceMonitor.Spec.Endpoints[len(serviceMonitor.Spec.Endpoints)-1].RelabelConfigs).To(Equal(valkey.Spec.Metrics.ServiceMonitor.Relabellings))
	Expect(serviceMonitor.Spec.Endpoints[len(serviceMonitor.Spec.Endpoints)-1].MetricRelabelConfigs).To(Equal(valkey.Spec.Metrics.ServiceMonitor.MetricRelabellings))
	Expect(serviceMonitor.Spec.Endpoints[len(serviceMonitor.Spec.Endpoints)-1].HonorLabels).To(Equal(valkey.Spec.Metrics.ServiceMonitor.HonorLabels))
	for k, v := range valkey.Spec.Metrics.ServiceMonitor.AdditionalLabels {
		Expect(serviceMonitor.ObjectMeta.Labels).Should(HaveKeyWithValue(k, v))
	}
	Expect(serviceMonitor.Spec.PodTargetLabels).To(ContainElements(valkey.Spec.Metrics.ServiceMonitor.PodTargetLabels))
}

func checkPrometheusRule(valkey *operatorv1alpha1.Valkey) {
	prometheusRule := &prometheusv1.PrometheusRule{}
	prometheusRuleName := fmt.Sprintf("valkey-%s", valkey.Name)
	err := cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: prometheusRuleName}, prometheusRule)
	Expect(err).NotTo(HaveOccurred())

	for k, v := range valkey.Spec.Metrics.PrometheusRule.AdditionalLabels {
		Expect(prometheusRule.ObjectMeta.Labels).Should(HaveKeyWithValue(k, v))
	}
	Expect(prometheusRule.Spec.Groups[0].Rules).To(Equal(valkey.Spec.Metrics.PrometheusRule.Rules))
}
