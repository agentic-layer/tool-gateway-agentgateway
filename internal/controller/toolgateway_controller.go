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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

const ToolGatewayKgatewayControllerName = "runtime.agentic-layer.ai/tool-gateway-kgateway-controller"

const (
	agentGatewayClassName = "agentgateway"
)

// Version set at build time using ldflags
var Version = "dev"

// ToolGatewayReconciler reconciles a ToolGateway object
type ToolGatewayReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolgateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolgateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolgatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewaybackends,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ToolGatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the ToolGateway instance
	var toolGateway agentruntimev1alpha1.ToolGateway
	if err := r.Get(ctx, req.NamespacedName, &toolGateway); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("ToolGateway resource not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get ToolGateway")
		return ctrl.Result{}, err
	}

	log.Info("Reconciling ToolGateway",
		"name", toolGateway.Name,
		"namespace", toolGateway.Namespace,
		"toolGatewayClass", toolGateway.Spec.ToolGatewayClassName)

	// Check if this controller should process this ToolGateway
	if !r.shouldProcessToolGateway(ctx, &toolGateway) {
		log.Info("Controller is not responsible for this ToolGateway, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// Create or update the Gateway for this ToolGateway
	if err := r.ensureGateway(ctx, &toolGateway); err != nil {
		log.Error(err, "Failed to ensure Gateway")
		return ctrl.Result{}, err
	}

	// Get all ToolServer resources
	toolServers, err := r.getToolServers(ctx)
	if err != nil {
		log.Error(err, "Failed to get ToolServers")
		return ctrl.Result{}, err
	}

	// Create or update resources for each ToolServer
	for _, toolServer := range toolServers {
		if err := r.ensureToolServerResources(ctx, &toolGateway, toolServer); err != nil {
			log.Error(err, "Failed to ensure ToolServer resources",
				"toolServerName", toolServer.Name,
				"toolServerNamespace", toolServer.Namespace)
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// shouldProcessToolGateway determines if this controller is responsible for the given ToolGateway
func (r *ToolGatewayReconciler) shouldProcessToolGateway(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) bool {
	log := logf.FromContext(ctx)

	// List all ToolGatewayClasses
	var toolGatewayClassList agentruntimev1alpha1.ToolGatewayClassList
	if err := r.List(ctx, &toolGatewayClassList); err != nil {
		log.Info("Cannot list ToolGatewayClasses, skipping to avoid errors")
		return false
	}

	// Filter to only classes managed by this controller
	var kgatewayClasses []agentruntimev1alpha1.ToolGatewayClass
	for _, tgc := range toolGatewayClassList.Items {
		if tgc.Spec.Controller == ToolGatewayKgatewayControllerName {
			kgatewayClasses = append(kgatewayClasses, tgc)
		}
	}

	// If className is explicitly set, check if it matches any of our managed classes
	toolGatewayClassName := toolGateway.Spec.ToolGatewayClassName
	if toolGatewayClassName != "" {
		for _, kgc := range kgatewayClasses {
			if kgc.Name == toolGatewayClassName {
				return true
			}
		}
	}

	// Look for ToolGatewayClass with default annotation among filtered classes
	for _, kgc := range kgatewayClasses {
		if kgc.Annotations["toolgatewayclass.kubernetes.io/is-default-class"] == "true" {
			return true
		}
	}

	return false
}

// getToolServers queries all ToolServer resources across all namespaces
func (r *ToolGatewayReconciler) getToolServers(ctx context.Context) ([]*agentruntimev1alpha1.ToolServer, error) {
	var toolServerList agentruntimev1alpha1.ToolServerList
	if err := r.List(ctx, &toolServerList); err != nil {
		return nil, err
	}

	toolServers := make([]*agentruntimev1alpha1.ToolServer, len(toolServerList.Items))
	for i := range toolServerList.Items {
		toolServers[i] = &toolServerList.Items[i]
	}

	return toolServers, nil
}

// ensureGateway creates or updates the Gateway for this ToolGateway
func (r *ToolGatewayReconciler) ensureGateway(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) error {
	log := logf.FromContext(ctx)

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      toolGateway.Name,
			Namespace: toolGateway.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, gateway, func() error {
		// Set owner reference
		if err := controllerutil.SetControllerReference(toolGateway, gateway, r.Scheme); err != nil {
			return err
		}

		// Set the gateway specification
		gateway.Spec = gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(agentGatewayClassName),
			Listeners: []gatewayv1.Listener{
				{
					Name:     gatewayv1.SectionName("http"),
					Protocol: gatewayv1.HTTPProtocolType,
					Port:     gatewayv1.PortNumber(80),
					AllowedRoutes: &gatewayv1.AllowedRoutes{
						Namespaces: &gatewayv1.RouteNamespaces{
							From: ptr.To(gatewayv1.NamespacesFromAll),
						},
					},
				},
			},
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update Gateway: %w", err)
	}

	log.Info("Gateway reconciled", "operation", op, "name", gateway.Name, "namespace", gateway.Namespace)
	return nil
}

// ensureToolServerResources creates or updates resources for a ToolServer
func (r *ToolGatewayReconciler) ensureToolServerResources(
	ctx context.Context,
	toolGateway *agentruntimev1alpha1.ToolGateway,
	toolServer *agentruntimev1alpha1.ToolServer,
) error {
	// Ensure AgentgatewayBackend
	if err := r.ensureAgentgatewayBackend(ctx, toolGateway, toolServer); err != nil {
		return fmt.Errorf("failed to ensure AgentgatewayBackend: %w", err)
	}

	// Ensure HTTPRoute
	if err := r.ensureHTTPRoute(ctx, toolGateway, toolServer); err != nil {
		return fmt.Errorf("failed to ensure HTTPRoute: %w", err)
	}

	return nil
}

// ensureAgentgatewayBackend creates or updates an AgentgatewayBackend for a ToolServer
func (r *ToolGatewayReconciler) ensureAgentgatewayBackend(
	ctx context.Context,
	_ *agentruntimev1alpha1.ToolGateway,
	toolServer *agentruntimev1alpha1.ToolServer,
) error {
	log := logf.FromContext(ctx)

	// Create AgentgatewayBackend as unstructured since CRD types are not yet available as Go module
	backend := &unstructured.Unstructured{}
	backend.SetAPIVersion("agentgateway.dev/v1alpha1")
	backend.SetKind("AgentgatewayBackend")
	backend.SetName(toolServer.Name)
	backend.SetNamespace(toolServer.Namespace)

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, backend, func() error {
		// Set the backend specification
		if err := unstructured.SetNestedMap(backend.Object, map[string]interface{}{
			"targets": []interface{}{
				map[string]interface{}{
					"name": "mcp-target",
					"static": map[string]interface{}{
						"host": fmt.Sprintf("%s.%s.svc.cluster.local",
							toolServer.Name, toolServer.Namespace),
						"port":     int64(toolServer.Spec.Port),
						"protocol": "StreamableHTTP",
					},
				},
			},
		}, "spec", "mcp"); err != nil {
			return fmt.Errorf("failed to set backend spec: %w", err)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update AgentgatewayBackend: %w", err)
	}

	log.Info("AgentgatewayBackend reconciled", "operation", op, "name", toolServer.Name, "namespace", toolServer.Namespace)
	return nil
}

// ensureHTTPRoute creates or updates an HTTPRoute for a ToolServer
func (r *ToolGatewayReconciler) ensureHTTPRoute(
	ctx context.Context,
	toolGateway *agentruntimev1alpha1.ToolGateway,
	toolServer *agentruntimev1alpha1.ToolServer,
) error {
	log := logf.FromContext(ctx)

	// Determine the path for this tool server
	path := toolServer.Spec.Path
	if path == "" {
		// Default path based on transport type
		if toolServer.Spec.TransportType == "sse" {
			path = "/sse"
		} else {
			path = "/mcp"
		}
	}

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      toolServer.Name,
			Namespace: toolServer.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, route, func() error {
		// Set owner reference to the ToolGateway
		if err := controllerutil.SetControllerReference(toolGateway, route, r.Scheme); err != nil {
			return err
		}

		// Set the route specification
		pathType := gatewayv1.PathMatchPathPrefix
		route.Spec = gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:      gatewayv1.ObjectName(toolGateway.Name),
						Namespace: ptr.To(gatewayv1.Namespace(toolGateway.Namespace)),
					},
				},
			},
			Rules: []gatewayv1.HTTPRouteRule{
				{
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Group:     ptr.To(gatewayv1.Group("agentgateway.dev")),
									Kind:      ptr.To(gatewayv1.Kind("AgentgatewayBackend")),
									Name:      gatewayv1.ObjectName(toolServer.Name),
									Namespace: ptr.To(gatewayv1.Namespace(toolServer.Namespace)),
								},
							},
						},
					},
					Matches: []gatewayv1.HTTPRouteMatch{
						{
							Path: &gatewayv1.HTTPPathMatch{
								Type:  &pathType,
								Value: ptr.To(path),
							},
						},
					},
				},
			},
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update HTTPRoute: %w", err)
	}

	log.Info("HTTPRoute reconciled", "operation", op, "name", route.Name, "namespace", route.Namespace)
	return nil
}

// findToolGatewaysForToolServer returns all ToolGateway resources that need to be reconciled
// when a ToolServer changes. Since all gateways discover ToolServers across all namespaces,
// any ToolServer change affects all ToolGateway resources.
func (r *ToolGatewayReconciler) findToolGatewaysForToolServer(ctx context.Context, _ client.Object) []ctrl.Request {
	var toolGatewayList agentruntimev1alpha1.ToolGatewayList
	if err := r.List(ctx, &toolGatewayList); err != nil {
		return []ctrl.Request{}
	}

	requests := make([]ctrl.Request, len(toolGatewayList.Items))
	for i, gw := range toolGatewayList.Items {
		requests[i] = ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      gw.Name,
				Namespace: gw.Namespace,
			},
		}
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *ToolGatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentruntimev1alpha1.ToolGateway{}).
		Owns(&gatewayv1.Gateway{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Watches(
			&agentruntimev1alpha1.ToolServer{},
			handler.EnqueueRequestsFromMapFunc(r.findToolGatewaysForToolServer),
		).
		Named(ToolGatewayKgatewayControllerName).
		Complete(r)
}
