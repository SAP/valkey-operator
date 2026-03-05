/*
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package operator

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"text/template"
	"time"

	"github.com/Masterminds/sprig/v3"
	operatorv1alpha1 "github.com/sap/valkey-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kyaml "sigs.k8s.io/yaml"
)

func reconcileBinding(ctx context.Context, client client.Client, valkey *operatorv1alpha1.Valkey) error {
	params := make(map[string]any)

	if valkey.Spec.Sentinel != nil && valkey.Spec.Sentinel.Enabled {
		params["sentinelEnabled"] = true
		params["host"] = fmt.Sprintf("valkey-%s.%s.svc.cluster.local", valkey.Name, valkey.Namespace)
		params["port"] = 6379
		params["sentinelHost"] = fmt.Sprintf("valkey-%s.%s.svc.cluster.local", valkey.Name, valkey.Namespace)
		params["sentinelPort"] = 26379
		params["primaryName"] = "myprimary"
	} else {
		params["primaryHost"] = fmt.Sprintf("valkey-%s-primary.%s.svc.cluster.local", valkey.Name, valkey.Namespace)
		params["primaryPort"] = 6379
		params["replicaHost"] = fmt.Sprintf("valkey-%s-replicas.%s.svc.cluster.local", valkey.Name, valkey.Namespace)
		params["replicaPort"] = 6379
	}

	authSecretName := fmt.Sprintf("valkey-%s", valkey.Name)
	password := ""
	authSecret := &corev1.Secret{}
	authSecretFound := true
	if err := client.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: authSecretName}, authSecret); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		authSecretFound = false
	} else if rawPassword, ok := authSecret.Data["valkey-password"]; ok {
		password = string(rawPassword)
	}
	if password == "" {
		password = deriveDefaultPassword(valkey.Namespace, authSecretName)
	}
	if !authSecretFound {
		authSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authSecretName,
				Namespace: valkey.Namespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"default":         []byte(password),
				"valkey-password": []byte(password),
			},
		}
		if err := client.Create(ctx, authSecret); err != nil {
			return err
		}
	} else {
		if authSecret.Data == nil {
			authSecret.Data = map[string][]byte{}
		}
		updated := false
		if _, ok := authSecret.Data["valkey-password"]; !ok {
			authSecret.Data["valkey-password"] = []byte(password)
			updated = true
		}
		if _, ok := authSecret.Data["default"]; !ok {
			authSecret.Data["default"] = []byte(password)
			updated = true
		}
		if updated {
			if err := client.Update(ctx, authSecret); err != nil {
				return err
			}
		}
	}
	params["password"] = password

	if valkey.Spec.TLS != nil && valkey.Spec.TLS.Enabled {
		params["tlsEnabled"] = true
		params["caData"] = ""
		tlsSecret := &corev1.Secret{}
		tlsSecretName := ""
		if valkey.Spec.TLS.CertManager == nil {
			tlsSecretName = fmt.Sprintf("valkey-%s-crt", valkey.Name)
		} else {
			tlsSecretName = fmt.Sprintf("valkey-%s-tls", valkey.Name)
		}
		tlsSecretFound := true
		if err := client.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: tlsSecretName}, tlsSecret); err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
			tlsSecretFound = false
		}

		// Create TLS secret if it doesn't exist
		if !tlsSecretFound {
			serviceName := fmt.Sprintf("valkey-%s", valkey.Name)
			caCert, tlsCert, tlsKey, err := generateSelfSignedCert(serviceName, valkey.Namespace)
			if err != nil {
				return fmt.Errorf("failed to generate TLS certificate: %w", err)
			}

			tlsSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      tlsSecretName,
					Namespace: valkey.Namespace,
				},
				Type: corev1.SecretTypeTLS,
				Data: map[string][]byte{
					"ca.crt":  caCert,
					"tls.crt": tlsCert,
					"tls.key": tlsKey,
				},
			}
			if err := client.Create(ctx, tlsSecret); err != nil {
				return fmt.Errorf("failed to create TLS secret: %w", err)
			}
		}

		if tlsSecret.Data != nil {
			if caCert, ok := tlsSecret.Data["ca.crt"]; ok {
				params["caData"] = string(caCert)
			}
		}
	}

	var buf bytes.Buffer
	t := template.New("binding.yaml").Option("missingkey=zero").Funcs(sprig.TxtFuncMap())
	if valkey.Spec.Binding != nil && valkey.Spec.Binding.Template != nil {
		if _, err := t.Parse(*valkey.Spec.Binding.Template); err != nil {
			return err
		}
	} else {
		if _, err := t.ParseFS(data, "data/binding.yaml"); err != nil {
			return err
		}
	}
	if err := t.Execute(&buf, params); err != nil {
		return err
	}

	var bindingData map[string]any
	if err := kyaml.Unmarshal(buf.Bytes(), &bindingData); err != nil {
		return err
	}

	bindingSecret := &corev1.Secret{}
	bindingSecretName := ""
	if valkey.Spec.Binding != nil && valkey.Spec.Binding.SecretName != "" {
		bindingSecretName = valkey.Spec.Binding.SecretName
	} else {
		bindingSecretName = fmt.Sprintf("valkey-%s-binding", valkey.Name)
	}
	if err := client.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: bindingSecretName}, bindingSecret); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		bindingSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bindingSecretName,
				Namespace: valkey.Namespace,
			},
			Type: corev1.SecretTypeOpaque,
		}
	}
	bindingSecret.Data = make(map[string][]byte)
	for key, value := range bindingData {
		if stringValue, ok := value.(string); ok {
			bindingSecret.Data[key] = []byte(stringValue)
		} else {
			rawValue, err := json.Marshal(value)
			if err != nil {
				return err
			}
			bindingSecret.Data[key] = rawValue
		}
	}
	// TODO: avoid this update call if not necessary (e.g. by checking if data have changed)
	if bindingSecret.CreationTimestamp.IsZero() {
		if err := client.Create(ctx, bindingSecret); err != nil {
			return err
		}
		return nil
	}
	if err := client.Update(ctx, bindingSecret); err != nil {
		return err
	}

	return nil
}

func deriveDefaultPassword(namespace, fullname string) string {
	seed := fmt.Sprintf("%s:%s:valkey-password", namespace, fullname)
	sum := sha256.Sum256([]byte(seed))
	hexSum := hex.EncodeToString(sum[:])
	if len(hexSum) > 32 {
		return hexSum[:32]
	}
	return hexSum
}

// generateSelfSignedCert generates a self-signed certificate for testing/development
func generateSelfSignedCert(serviceName, namespace string) (caCert, tlsCert, tlsKey []byte, err error) {
	// Generate CA private key
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate CA key: %w", err)
	}

	// Create CA certificate template
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Valkey Operator"},
			CommonName:   "Valkey CA",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0), // 10 years
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	// Create CA certificate
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create CA certificate: %w", err)
	}

	// PEM encode CA certificate
	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})

	// Generate server private key
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate server key: %w", err)
	}

	// Create server certificate template
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			Organization: []string{"Valkey Operator"},
			CommonName:   serviceName,
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().AddDate(10, 0, 0), // 10 years
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:    []string{serviceName, fmt.Sprintf("%s.%s", serviceName, namespace), fmt.Sprintf("%s.%s.svc", serviceName, namespace), fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace)},
	}

	// Create server certificate signed by CA
	serverCertDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create server certificate: %w", err)
	}

	// PEM encode server certificate
	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCertDER})

	// PEM encode server private key
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)})

	return caCertPEM, serverCertPEM, serverKeyPEM, nil
}
