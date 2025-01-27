/*
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package operator

import (
	"embed"
	"flag"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/sap/component-operator-runtime/pkg/component"
	"github.com/sap/component-operator-runtime/pkg/manifests"
	"github.com/sap/component-operator-runtime/pkg/manifests/helm"
	"github.com/sap/component-operator-runtime/pkg/operator"

	operatorv1alpha1 "github.com/sap/valkey-operator/api/v1alpha1"
	"github.com/sap/valkey-operator/internal/transformer"
)

const Name = "valkey-operator.cs.sap.com"

//go:embed all:data
var data embed.FS

type Options struct {
	Name       string
	FlagPrefix string
}

type Operator struct {
	options Options
}

var defaultOperator operator.Operator = New()

func GetName() string {
	return defaultOperator.GetName()
}

func InitScheme(scheme *runtime.Scheme) {
	defaultOperator.InitScheme(scheme)
}

func InitFlags(flagset *flag.FlagSet) {
	defaultOperator.InitFlags(flagset)
}

func ValidateFlags() error {
	return defaultOperator.ValidateFlags()
}

func GetUncacheableTypes() []client.Object {
	return defaultOperator.GetUncacheableTypes()
}

func Setup(mgr ctrl.Manager) error {
	return defaultOperator.Setup(mgr)
}

func New() *Operator {
	return NewWithOptions(Options{})
}

func NewWithOptions(options Options) *Operator {
	operator := &Operator{options: options}
	if operator.options.Name == "" {
		operator.options.Name = Name
	}
	return operator
}

func (o *Operator) GetName() string {
	return o.options.Name
}

func (o *Operator) InitScheme(scheme *runtime.Scheme) {
	utilruntime.Must(operatorv1alpha1.AddToScheme(scheme))
}

func (o *Operator) InitFlags(flagset *flag.FlagSet) {
	// Add logic to initialize flags (if running in a combined controller you might want to evaluate o.options.FlagPrefix).
}

func (o *Operator) ValidateFlags() error {
	// Add logic to validate flags (if running in a combined controller you might want to evaluate o.options.FlagPrefix).
	return nil
}

func (o *Operator) GetUncacheableTypes() []client.Object {
	// Add types which should bypass informer caching.
	return []client.Object{&operatorv1alpha1.Valkey{}}
}

func (o *Operator) Setup(mgr ctrl.Manager) error {
	parameterTransformer, err := manifests.NewTemplateParameterTransformer(data, "data/parameters.yaml")
	if err != nil {
		return errors.Wrap(err, "error initializing parameter transformer")
	}
	objectTransformer := transformer.NewObjectTransformer()
	resourceGenerator, err := helm.NewTransformableHelmGenerator(
		data,
		"data/charts/valkey",
		mgr.GetClient(),
	)
	if err != nil {
		return errors.Wrap(err, "error initializing resource generator")
	}
	resourceGenerator.
		WithParameterTransformer(parameterTransformer).
		WithObjectTransformer(objectTransformer)

	// TODO: handle increases of persistence.size somehow (instead of making it immutable)
	// this would require to recreate the statefulset (since persistentVolumeClaimTemplate is immutable)
	// and to extend existing persistent volume claims (supposing that they are resizable)

	if err := component.NewReconciler[*operatorv1alpha1.Valkey](
		o.options.Name,
		resourceGenerator,
		component.ReconcilerOptions{},
	).WithPostReconcileHook(
		reconcileBinding,
	).SetupWithManager(mgr); err != nil {
		return errors.Wrapf(err, "unable to create controller")
	}

	return nil
}
