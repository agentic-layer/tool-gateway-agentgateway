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
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

const toolGatewayClassDefaultAnnotation = "toolgatewayclass.kubernetes.io/is-default-class"

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
