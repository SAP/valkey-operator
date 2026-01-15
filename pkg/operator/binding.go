/*
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	operatorv1alpha1 "github.com/sap/valkey-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
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

	authSecret := &corev1.Secret{}
	authSecretName := fmt.Sprintf("valkey-%s", valkey.Name)
	if err := client.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: authSecretName}, authSecret); err != nil {
		return err
	}
	params["password"] = string(authSecret.Data["valkey-password"])

	if valkey.Spec.TLS != nil && valkey.Spec.TLS.Enabled {
		params["tlsEnabled"] = true
		tlsSecret := &corev1.Secret{}
		tlsSecretName := ""
		if valkey.Spec.TLS.CertManager == nil {
			tlsSecretName = fmt.Sprintf("valkey-%s-crt", valkey.Name)
		} else {
			tlsSecretName = fmt.Sprintf("valkey-%s-tls", valkey.Name)
		}
		if err := client.Get(ctx, types.NamespacedName{Namespace: valkey.Namespace, Name: tlsSecretName}, tlsSecret); err != nil {
			return err
		}
		params["caData"] = string(tlsSecret.Data["ca.crt"])
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
		return err
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
	if err := client.Update(ctx, bindingSecret); err != nil {
		return err
	}

	return nil
}
