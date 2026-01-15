/*
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

import (
	"context"
	"fmt"
	"text/template"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/Masterminds/sprig/v3"
	"github.com/pkg/errors"
	"github.com/sap/admission-webhook-runtime/pkg/admission"
)

// +kubebuilder:object:generate=false
type Webhook struct {
}

var _ admission.ValidatingWebhook[*Valkey] = &Webhook{}

func NewWebhook() *Webhook {
	return &Webhook{}
}

func (w *Webhook) ValidateCreate(ctx context.Context, valkey *Valkey) error {
	if err := validateTemplate(valkey); err != nil {
		return err
	}
	return nil
}

func (w *Webhook) ValidateUpdate(ctx context.Context, oldValkey *Valkey, valkey *Valkey) error {
	if (oldValkey.Spec.Sentinel != nil && oldValkey.Spec.Sentinel.Enabled) != (valkey.Spec.Sentinel != nil && valkey.Spec.Sentinel.Enabled) {
		return fmt.Errorf(".spec.sentinel.enabled is immutable")
	}
	if (oldValkey.Spec.Persistence != nil && oldValkey.Spec.Persistence.Enabled) != (valkey.Spec.Persistence != nil && valkey.Spec.Persistence.Enabled) {
		return fmt.Errorf(".spec.persistence.enabled is immutable")
	}
	if oldValkey.Spec.Persistence == nil && valkey.Spec.Persistence != nil && valkey.Spec.Persistence.Size != nil ||
		oldValkey.Spec.Persistence != nil && oldValkey.Spec.Persistence.Size != nil && valkey.Spec.Persistence == nil ||
		oldValkey.Spec.Persistence != nil && valkey.Spec.Persistence != nil && !quantityEqual(oldValkey.Spec.Persistence.Size, valkey.Spec.Persistence.Size) {
		return fmt.Errorf(".spec.persistence.size is immutable")
	}
	if oldValkey.Spec.Persistence == nil && valkey.Spec.Persistence != nil && valkey.Spec.Persistence.StorageClass != "" ||
		oldValkey.Spec.Persistence != nil && oldValkey.Spec.Persistence.StorageClass != "" && valkey.Spec.Persistence == nil ||
		oldValkey.Spec.Persistence != nil && valkey.Spec.Persistence != nil && oldValkey.Spec.Persistence.StorageClass != valkey.Spec.Persistence.StorageClass {
		return fmt.Errorf(".spec.persistence.storageClass is immutable")
	}
	if err := validateTemplate(valkey); err != nil {
		return err
	}
	return nil
}

func (w *Webhook) ValidateDelete(ctx context.Context, valkey *Valkey) error {
	return nil
}

func (w *Webhook) SetupWithManager(mgr manager.Manager) {
	mgr.GetWebhookServer().Register(
		fmt.Sprintf("/admission/%s/valkey/validate", GroupVersion),
		admission.NewValidatingWebhookHandler[*Valkey](w, mgr.GetScheme(), mgr.GetLogger().WithName("webhook-runtime")),
	)
}

func validateTemplate(valkey *Valkey) error {
	if valkey.Spec.Binding != nil && valkey.Spec.Binding.Template != nil {
		t := template.New("binding.yaml").Option("missingkey=zero").Funcs(sprig.TxtFuncMap())
		if _, err := t.Parse(*valkey.Spec.Binding.Template); err != nil {
			return errors.Wrapf(err, "specified .spec.binding.template is invalid")
		}
	}
	return nil
}

func quantityEqual(x *resource.Quantity, y *resource.Quantity) bool {
	if x == nil && y == nil {
		return true
	}
	if x == nil && y != nil || x != nil && y == nil {
		return false
	}
	return *x == *y
}
