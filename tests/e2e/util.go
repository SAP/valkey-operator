/*
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorv1alpha1 "github.com/sap/valkey-operator/api/v1alpha1"
)

const (
	CertManagerVersion        = "v1.12.1"
	PrometheusOperatorVersion = "v0.73.1"
)

// run command and print stdout/stderr
func run(ctx context.Context, name string, arg ...string) error {
	cmd := exec.CommandContext(ctx, name, arg...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// apply a list of kubernetes manifests to cluster
func applyManifests(ctx context.Context, cli client.Client, manifests []byte) error {
	var objects []*unstructured.Unstructured
	decoder := utilyaml.NewYAMLToJSONDecoder(bytes.NewBuffer(manifests))
	for {
		object := &unstructured.Unstructured{}
		if err := decoder.Decode(&object.Object); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if object.Object == nil {
			continue
		}
		objects = append(objects, object)
	}
	for _, object := range objects {
		if err := cli.Create(ctx, object); err != nil {
			return err
		}
	}
	return nil
}

// create kind cluster
func createKindCluster(ctx context.Context, kind string, name string, kubeconfig string) (err error) {
	if kind == "" {
		return fmt.Errorf("no kind executable was found or provided")
	}
	if err := run(ctx, kind, "version"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			deleteKindCluster(kind, name)
		}
	}()
	if err := run(ctx, kind, "create", "cluster", "--name", name, "--kubeconfig", kubeconfig, "--wait", "300s"); err != nil {
		return err
	}

	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}, nil).ClientConfig()
	if err != nil {
		return err
	}
	cli, err := client.New(cfg, client.Options{})
	if err != nil {
		return err
	}
	err = downloadAndApplyManifests(ctx, cli, fmt.Sprintf("https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml", CertManagerVersion))
	if err != nil {
		return err
	}
	err = downloadAndApplyManifests(ctx, cli, fmt.Sprintf("https://github.com/prometheus-operator/prometheus-operator/releases/download/%s/stripped-down-crds.yaml", PrometheusOperatorVersion))
	if err != nil {
		return err
	}

	return nil
}

func downloadAndApplyManifests(ctx context.Context, cli client.Client, url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	manifests, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return applyManifests(ctx, cli, manifests)
}

// delete kind cluster
func deleteKindCluster(kind string, name string) error {
	if kind == "" {
		return fmt.Errorf("no kind executable was found or provided")
	}
	if err := run(context.Background(), kind, "version"); err != nil {
		return err
	}
	if err := run(context.Background(), kind, "delete", "cluster", "--name", name); err != nil {
		return err
	}

	return nil
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
					Resources:   []string{"valkey"},
				},
			}},
			SideEffects: &[]admissionv1.SideEffectClass{admissionv1.SideEffectClassNone}[0],
		}},
	}
}

// valkey node descriptor
type ValkeyNode struct {
	IP        string
	Port      uint16
	PodName   string
	LocalPort uint16
}

// valkey node map
type ValkeyNodeMap map[string]*ValkeyNode

// add endpoints of given service to valkey node map
func addServiceToValkeyNodeMap(ctx context.Context, cli client.Client, namespace string, name string, valkeyNodeMap ValkeyNodeMap) error {
	service := &corev1.Service{}
	if err := cli.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, service); err != nil {
		return err
	}
	endpoints := &corev1.Endpoints{}
	if err := cli.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, endpoints); err != nil {
		return err
	}
	for _, servicePort := range service.Spec.Ports {
		if servicePort.Protocol != "" && servicePort.Protocol != "TCP" {
			continue
		}
		for _, subset := range endpoints.Subsets {
			for _, endpointPort := range subset.Ports {
				if endpointPort.Name == servicePort.Name {
					for _, endpointAddress := range subset.Addresses {
						if endpointAddress.TargetRef.Kind != "Pod" || endpointAddress.TargetRef.Namespace != namespace {
							continue
						}
						key := fmt.Sprintf("%s:%d", endpointAddress.IP, endpointPort.Port)
						node, ok := valkeyNodeMap[key]
						if ok {
							if node.IP != endpointAddress.IP || node.Port != uint16(endpointPort.Port) || node.PodName != endpointAddress.TargetRef.Name {
								panic("this cannot happen")
							}
						} else {
							node = &ValkeyNode{
								IP:      endpointAddress.IP,
								Port:    uint16(endpointPort.Port),
								PodName: endpointAddress.TargetRef.Name,
							}
							valkeyNodeMap[key] = node
						}
						if endpointAddress.Hostname != "" {
							valkeyNodeMap[fmt.Sprintf("%s.%s.%s.svc.cluster.local:%d", endpointAddress.Hostname, name, namespace, endpointPort.Port)] = node
						}
						valkeyNodeMap[fmt.Sprintf("%s.%s.svc.cluster.local:%d", name, namespace, servicePort.Port)] = node
					}
				}
			}
		}
	}
	return nil
}

// start a port forwarding for a pod
func startPortForwarding(ctx context.Context, cfg *rest.Config, namespace string, name string, port uint16) (uint16, error) {
	ctx, cancel := context.WithCancel(ctx)
	_ = cancel // appease go vet (parent context will be cancelled outside)

	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return 0, err
	}

	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, name)
	host := strings.TrimPrefix(cfg.Host, "https://")
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, &url.URL{Scheme: "https", Path: path, Host: host})

	readyCh := make(chan struct{})
	errorCh := make(chan error)
	fw, err := portforward.New(dialer, []string{fmt.Sprintf(":%d", port)}, ctx.Done(), readyCh, io.Discard, io.Discard)
	if err != nil {
		return 0, err
	}
	go func() {
		if err := fw.ForwardPorts(); err != nil {
			errorCh <- err
		}
	}()

	select {
	case <-readyCh:
		ports, err := fw.GetPorts()
		if err != nil {
			return 0, nil
		}
		return ports[0].Local, nil
	case err := <-errorCh:
		cancel()
		return 0, err
	case <-time.After(10 * time.Second):
		cancel()
		return 0, fmt.Errorf("error creating port forward to %s/%s:%d (timeout)", namespace, name, port)
	}
}

// generate a CA keypair
func generateCertificateAuthority() ([]byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, nil, err
	}
	keyPem := new(bytes.Buffer)
	pem.Encode(keyPem, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	cert := &x509.Certificate{
		SerialNumber:          big.NewInt(0),
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(0, 0, 1),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	certDer, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	certPem := new(bytes.Buffer)
	pem.Encode(certPem, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDer,
	})

	return keyPem.Bytes(), certPem.Bytes(), nil
}
