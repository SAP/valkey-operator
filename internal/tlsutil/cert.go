/*
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package tlsutil

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// GenerateSelfSignedCert creates a CA and server cert for in-cluster TLS usage.
func GenerateSelfSignedCert(serviceName, namespace string) (caCert, tlsCert, tlsKey []byte, err error) {
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate CA key: %w", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Valkey Operator"},
			CommonName:   "Valkey CA",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create CA certificate: %w", err)
	}

	tlsKeyObj, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate TLS key: %w", err)
	}

	tlsTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			Organization: []string{"Valkey Operator"},
			CommonName:   serviceName,
		},
		DNSNames: []string{
			serviceName,
			fmt.Sprintf("%s.%s", serviceName, namespace),
			fmt.Sprintf("%s.%s.svc", serviceName, namespace),
			fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace),
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	tlsCertDER, err := x509.CreateCertificate(rand.Reader, tlsTemplate, caTemplate, &tlsKeyObj.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create TLS certificate: %w", err)
	}

	caCert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})
	tlsCert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsCertDER})
	tlsKey = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(tlsKeyObj)})

	return caCert, tlsCert, tlsKey, nil
}
