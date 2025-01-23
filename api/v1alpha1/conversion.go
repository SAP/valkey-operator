/*
Copyright 2025 SAP SE.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

// Uncomment the following block if conversion is used, and this api version is the conversion hub;
// see https://book.kubebuilder.io/multiversion-tutorial/conversion.html to learn about the concept of hubs and spokes.
/*
import "sigs.k8s.io/controller-runtime/pkg/conversion"

var _ conversion.Hub = &Valkey{}

func (c *Valkey) Hub() {}
*/

// Uncomment the following block if conversion is used, and this api version is a conversion spoke,
// and replace _HUB_API_VERSION_ with the api version of the conversion hub;
// see https://book.kubebuilder.io/multiversion-tutorial/conversion.html to learn about the concept of hubs and spokes.
/*
import (
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	"github.com/sap/valkey-operator/api/_HUB_API_VERSION_"
)

var _ conversion.Convertible = &Valkey{}

func (src *Valkey) ConvertTo(dstHub conversion.Hub) error {
	dst := dstHub.(*_HUB_API_VERSION_.Valkey)
	dst.ObjectMeta = src.ObjectMeta
	// Add logic here to convert src.Spec into dst.Spec and src.Status into dst.Status.
	return nil
}

func (dst *Valkey) ConvertFrom(srcHub conversion.Hub) error {
	src := srcHub.(*_HUB_API_VERSION_.Valkey)
	dst.ObjectMeta = src.ObjectMeta
	// Add logic here to convert src.Spec into dst.Spec and src.Status into dst.Status.
	return nil
}
*/
