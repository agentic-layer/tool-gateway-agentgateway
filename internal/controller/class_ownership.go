/*
Copyright 2026.

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

package controller

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

const toolGatewayClassDefaultAnnotation = "toolgatewayclass.kubernetes.io/is-default-class"

// defaultToolGatewayNamespace is the well-known namespace searched for a
// default ToolGateway when a ToolRoute has no explicit ToolGatewayRef. This
// mirrors agent-runtime-operator's AiGateway defaulting strategy (fixed
// "ai-gateway" namespace) for consistency across the agentic-layer stack.
const defaultToolGatewayNamespace = "tool-gateway"

// errNoDefaultToolGateway is returned when a ToolRoute omits ToolGatewayRef
// and no ToolGateway exists in the well-known default namespace.
var errNoDefaultToolGateway = errors.New("no default ToolGateway found in namespace " + defaultToolGatewayNamespace)

// resolveToolGatewayKey returns the namespaced name of the ToolGateway that
// the given ToolRoute targets.
//
// When ToolGatewayRef is set, the explicit reference is returned (with
// namespace defaulting to the route's namespace).
//
// When ToolGatewayRef is nil, the function returns the first ToolGateway
// found in the well-known defaultToolGatewayNamespace. If multiple exist, the
// first listed item wins (a warning is logged). If none exist, returns
// errNoDefaultToolGateway.
func resolveToolGatewayKey(ctx context.Context, c client.Reader, route *agentruntimev1alpha1.ToolRoute) (types.NamespacedName, error) {
	if route.Spec.ToolGatewayRef != nil {
		ns := route.Spec.ToolGatewayRef.Namespace
		if ns == "" {
			ns = route.Namespace
		}
		return types.NamespacedName{Name: route.Spec.ToolGatewayRef.Name, Namespace: ns}, nil
	}

	var tgList agentruntimev1alpha1.ToolGatewayList
	if err := c.List(ctx, &tgList, client.InNamespace(defaultToolGatewayNamespace)); err != nil {
		return types.NamespacedName{}, fmt.Errorf("failed to list ToolGateways in namespace %s: %w", defaultToolGatewayNamespace, err)
	}

	if len(tgList.Items) == 0 {
		return types.NamespacedName{}, errNoDefaultToolGateway
	}

	if len(tgList.Items) > 1 {
		logf.FromContext(ctx).Info("Multiple ToolGateways found in default namespace, selecting first one",
			"namespace", defaultToolGatewayNamespace,
			"selected", tgList.Items[0].Name,
			"count", len(tgList.Items))
	}

	tg := tgList.Items[0]
	return types.NamespacedName{Name: tg.Name, Namespace: tg.Namespace}, nil
}

// isToolGatewayOwnedByController reports whether the given ToolGateway is
// owned by this controller. An explicit spec.toolGatewayClassName must match a
// ToolGatewayClass whose controller is ours; when the field is empty, the
// ToolGateway is claimed iff a ToolGatewayClass owned by this controller
// carries the default-class annotation.
//
// Returns (false, error) only when listing ToolGatewayClasses fails, so
// callers can requeue instead of silently dropping the object.
func isToolGatewayOwnedByController(ctx context.Context, c client.Reader, tg *agentruntimev1alpha1.ToolGateway) (bool, error) {
	var classList agentruntimev1alpha1.ToolGatewayClassList
	if err := c.List(ctx, &classList); err != nil {
		return false, fmt.Errorf("failed to list ToolGatewayClasses: %w", err)
	}

	owned := make([]agentruntimev1alpha1.ToolGatewayClass, 0, len(classList.Items))
	for _, tgc := range classList.Items {
		if tgc.Spec.Controller == ToolGatewayAgentgatewayControllerName {
			owned = append(owned, tgc)
		}
	}

	if className := tg.Spec.ToolGatewayClassName; className != "" {
		for _, c := range owned {
			if c.Name == className {
				return true, nil
			}
		}
		return false, nil
	}

	for _, c := range owned {
		if c.Annotations[toolGatewayClassDefaultAnnotation] == "true" {
			return true, nil
		}
	}
	return false, nil
}
