/*
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package operator_test

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sap/component-operator-runtime/pkg/component"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	operatorv1alpha1 "github.com/sap/valkey-operator/api/v1alpha1"
	"github.com/sap/valkey-operator/pkg/operator"
)

func TestOperator(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Operator")
}

var testEnv *envtest.Environment
var cfg *rest.Config
var cli client.Client
var ctx context.Context
var cancel context.CancelFunc
var threads sync.WaitGroup
var tmpdir string

var _ = BeforeSuite(func() {
	var err error

	By("initializing")
	log.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())
	tmpdir, err = os.MkdirTemp("", "")
	Expect(err).NotTo(HaveOccurred())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{"../../crds"},
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			ValidatingWebhooks: []*admissionv1.ValidatingWebhookConfiguration{
				buildValidatingWebhookConfiguration(),
			},
		},
	}
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())
	webhookInstallOptions := &testEnv.WebhookInstallOptions

	err = clientcmd.WriteToFile(*kubeConfigFromRestConfig(cfg), fmt.Sprintf("%s/kubeconfig", tmpdir))
	Expect(err).NotTo(HaveOccurred())
	fmt.Printf("A temporary kubeconfig for the envtest environment can be found here: %s/kubeconfig\n", tmpdir)

	By("populating scheme")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(apiregistrationv1.AddToScheme(scheme))
	operator.InitScheme(scheme)

	By("initializing client")
	cli, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())

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

	By("starting dummy controller-manager")
	threads.Add(1)
	go func() {
		defer threads.Done()
		defer GinkgoRecover()
		// since there is no controller-manager in envtest, we fake all statefulsets into a ready state
		// such that kstatus recognizes them as 'Current'
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
				statefulSetList := &appsv1.StatefulSetList{}
				err := cli.List(context.Background(), statefulSetList)
				Expect(err).NotTo(HaveOccurred())
				for _, statefulSet := range statefulSetList.Items {
					if statefulSet.DeletionTimestamp.IsZero() {
						status := &statefulSet.Status
						oldStatus := status.DeepCopy()
						status.ObservedGeneration = statefulSet.Generation
						status.Replicas = *statefulSet.Spec.Replicas
						status.ReadyReplicas = *statefulSet.Spec.Replicas
						status.AvailableReplicas = *statefulSet.Spec.Replicas
						status.CurrentReplicas = *statefulSet.Spec.Replicas
						status.UpdatedReplicas = *statefulSet.Spec.Replicas
						if reflect.DeepEqual(status, oldStatus) {
							continue
						}
						err = cli.Status().Update(context.Background(), &statefulSet)
						if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
							err = nil
						}
						Expect(err).NotTo(HaveOccurred())
					} else {
						if controllerutil.RemoveFinalizer(&statefulSet, metav1.FinalizerDeleteDependents) {
							err = cli.Update(context.Background(), &statefulSet)
							if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
								err = nil
							}
							Expect(err).NotTo(HaveOccurred())
						}
					}
				}
			}
		}
	}()

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
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	threads.Wait()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
	err = os.RemoveAll(tmpdir)
	Expect(err).NotTo(HaveOccurred())
})

var _ = Describe("Deploy Valkey", func() {
	var namespace string

	BeforeEach(func() {
		namespace = createNamespace()
	})

	It("should deploy Valkey with one primary and zero read replicas, with TLS disabled", func() {
		valkey := &operatorv1alpha1.Valkey{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      "test",
			},
		}

		err := cli.Create(ctx, valkey)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() error {
			if err := cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: valkey.Name}, valkey); err != nil {
				return err
			}
			if valkey.Status.ObservedGeneration != valkey.Generation || valkey.Status.State != component.StateReady {
				return fmt.Errorf("again")
			}
			return nil
		}, "10s", "100ms").Should(Succeed())

		authSecret := &corev1.Secret{}
		err = cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: fmt.Sprintf("valkey-%s", valkey.Name)}, authSecret)
		Expect(err).NotTo(HaveOccurred())

		bindingSecret := &corev1.Secret{}
		err = cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: fmt.Sprintf("valkey-%s-binding", valkey.Name)}, bindingSecret)
		Expect(err).NotTo(HaveOccurred())
		expectedSecretData := map[string][]byte{
			"primaryHost": []byte(fmt.Sprintf("valkey-%s-primary.%s.svc.cluster.local", valkey.Name, valkey.Namespace)),
			"primaryPort": []byte("6379"),
			"replicaHost": []byte(fmt.Sprintf("valkey-%s-replicas.%s.svc.cluster.local", valkey.Name, valkey.Namespace)),
			"replicaPort": []byte("6379"),
			"password":    authSecret.Data["valkey-password"],
		}
		Expect(bindingSecret.Data).To(Equal(expectedSecretData))

		// validate that sentinel.enabled is immutable
		_valkey := valkey.DeepCopy()
		_valkey.Spec.Sentinel = &operatorv1alpha1.SentinelProperties{Enabled: true}
		err = cli.Update(ctx, _valkey)
		Expect(apierrors.IsForbidden(err)).To(BeTrue())

		// validate that persistence.enabled is immutable
		_valkey = valkey.DeepCopy()
		_valkey.Spec.Persistence = &operatorv1alpha1.PersistenceProperties{Enabled: true}
		err = cli.Update(ctx, _valkey)
		Expect(apierrors.IsForbidden(err)).To(BeTrue())
	})

	It("should deploy Valkey with sentinel (three nodes), with metrics, TLS, persistence enabled", func() {
		valkey := &operatorv1alpha1.Valkey{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      "test",
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

		err := cli.Create(ctx, valkey)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() error {
			if err := cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: valkey.Name}, valkey); err != nil {
				return err
			}
			if valkey.Status.ObservedGeneration != valkey.Generation || valkey.Status.State != component.StateReady {
				return fmt.Errorf("again")
			}
			return nil
		}, "10s", "100ms").Should(Succeed())

		authSecret := &corev1.Secret{}
		err = cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: fmt.Sprintf("valkey-%s", valkey.Name)}, authSecret)
		Expect(err).NotTo(HaveOccurred())

		tlsSecret := &corev1.Secret{}
		err = cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: fmt.Sprintf("valkey-%s-crt", valkey.Name)}, tlsSecret)
		Expect(err).NotTo(HaveOccurred())

		bindingSecret := &corev1.Secret{}
		err = cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: fmt.Sprintf("valkey-%s-binding", valkey.Name)}, bindingSecret)
		Expect(err).NotTo(HaveOccurred())
		expectedSecretData := map[string][]byte{
			"sentinelEnabled": []byte("true"),
			"host":            []byte(fmt.Sprintf("valkey-%s.%s.svc.cluster.local", valkey.Name, valkey.Namespace)),
			"port":            []byte("6379"),
			"sentinelHost":    []byte(fmt.Sprintf("valkey-%s.%s.svc.cluster.local", valkey.Name, valkey.Namespace)),
			"sentinelPort":    []byte("26379"),
			"primaryName":     []byte("myprimary"),
			"password":        authSecret.Data["valkey-password"],
			"tlsEnabled":      []byte("true"),
			"caData":          tlsSecret.Data["ca.crt"],
		}
		Expect(bindingSecret.Data).To(Equal(expectedSecretData))

		// validate that sentinel.enabled is immutable
		_valkey := valkey.DeepCopy()
		_valkey.Spec.Sentinel = &operatorv1alpha1.SentinelProperties{Enabled: false}
		err = cli.Update(ctx, _valkey)
		Expect(apierrors.IsForbidden(err)).To(BeTrue())
		_valkey = valkey.DeepCopy()
		_valkey.Spec.Sentinel = nil
		err = cli.Update(ctx, _valkey)
		Expect(apierrors.IsForbidden(err)).To(BeTrue())

		// validate that persistence.enabled is immutable
		_valkey = valkey.DeepCopy()
		_valkey.Spec.Persistence = &operatorv1alpha1.PersistenceProperties{Enabled: false}
		err = cli.Update(ctx, _valkey)
		Expect(apierrors.IsForbidden(err)).To(BeTrue())
		_valkey = valkey.DeepCopy()
		_valkey.Spec.Persistence = nil
		err = cli.Update(ctx, _valkey)
		Expect(apierrors.IsForbidden(err)).To(BeTrue())
	})

	It("should deploy Valkey with one primary and some read replicas, with a custom binding template", func() {
		valkey := &operatorv1alpha1.Valkey{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      "test",
			},
			Spec: operatorv1alpha1.ValkeySpec{
				Replicas: 2,
				Binding: &operatorv1alpha1.BindingProperties{
					Template: &[]string{"primaryAddress: {{ .primaryHost }}:{{ .primaryPort }}\nreplicaAddress: {{ .replicaHost }}:{{ .replicaPort }}\n"}[0],
				},
			},
		}

		err := cli.Create(ctx, valkey)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() error {
			if err := cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: valkey.Name}, valkey); err != nil {
				return err
			}
			if valkey.Status.ObservedGeneration != valkey.Generation || valkey.Status.State != component.StateReady {
				return fmt.Errorf("again")
			}
			return nil
		}, "10s", "100ms").Should(Succeed())

		bindingSecret := &corev1.Secret{}
		err = cli.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: fmt.Sprintf("valkey-%s-binding", valkey.Name)}, bindingSecret)
		Expect(err).NotTo(HaveOccurred())
		expectedSecretData := map[string][]byte{
			"primaryAddress": []byte(fmt.Sprintf("valkey-%s-primary.%s.svc.cluster.local:%d", valkey.Name, valkey.Namespace, 6379)),
			"replicaAddress": []byte(fmt.Sprintf("valkey-%s-replicas.%s.svc.cluster.local:%d", valkey.Name, valkey.Namespace, 6379)),
		}
		Expect(bindingSecret.Data).To(Equal(expectedSecretData))
	})
})

func createNamespace() string {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ns-"}}
	err := cli.Create(ctx, namespace)
	Expect(err).NotTo(HaveOccurred())
	return namespace.Name
}

// assemble validatingwebhookconfiguration descriptor
func buildValidatingWebhookConfiguration() *admissionv1.ValidatingWebhookConfiguration {
	return &admissionv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: "validate-valkey",
		},
		Webhooks: []admissionv1.ValidatingWebhook{{
			Name:                    "validate-valkey.test.local",
			AdmissionReviewVersions: []string{"v1"},
			ClientConfig: admissionv1.WebhookClientConfig{
				Service: &admissionv1.ServiceReference{
					Path: &[]string{fmt.Sprintf("/admission/%s/valkey/validate", operatorv1alpha1.GroupVersion)}[0],
				},
			},
			Rules: []admissionv1.RuleWithOperations{{
				Operations: []admissionv1.OperationType{
					admissionv1.Create,
					admissionv1.Update,
					admissionv1.Delete,
				},
				Rule: admissionv1.Rule{
					APIGroups:   []string{operatorv1alpha1.GroupVersion.Group},
					APIVersions: []string{operatorv1alpha1.GroupVersion.Version},
					Resources:   []string{"valkeys"},
				},
			}},
			SideEffects: &[]admissionv1.SideEffectClass{admissionv1.SideEffectClassNone}[0],
		}},
	}
}

// convert rest.Config into kubeconfig
func kubeConfigFromRestConfig(restConfig *rest.Config) *clientcmdapi.Config {
	apiConfig := clientcmdapi.NewConfig()

	apiConfig.Clusters["envtest"] = clientcmdapi.NewCluster()
	cluster := apiConfig.Clusters["envtest"]
	cluster.Server = restConfig.Host
	cluster.CertificateAuthorityData = restConfig.CAData

	apiConfig.AuthInfos["envtest"] = clientcmdapi.NewAuthInfo()
	authInfo := apiConfig.AuthInfos["envtest"]
	authInfo.ClientKeyData = restConfig.KeyData
	authInfo.ClientCertificateData = restConfig.CertData

	apiConfig.Contexts["envtest"] = clientcmdapi.NewContext()
	context := apiConfig.Contexts["envtest"]
	context.Cluster = "envtest"
	context.AuthInfo = "envtest"

	apiConfig.CurrentContext = "envtest"

	return apiConfig
}
