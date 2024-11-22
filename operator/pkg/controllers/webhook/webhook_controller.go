package webhook

import (
	"context"
	"embed"
	"fmt"
	"reflect"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	globalhubv1alpha4 "github.com/stolostron/multicluster-global-hub/operator/api/operator/v1alpha4"
	"github.com/stolostron/multicluster-global-hub/operator/pkg/config"
	"github.com/stolostron/multicluster-global-hub/operator/pkg/deployer"
	"github.com/stolostron/multicluster-global-hub/operator/pkg/renderer"
	"github.com/stolostron/multicluster-global-hub/operator/pkg/utils"
	"github.com/stolostron/multicluster-global-hub/pkg/constants"
	"github.com/stolostron/multicluster-global-hub/pkg/logger"
)

//go:embed manifests
var fs embed.FS

var (
	log                      = logger.DefaultZapLogger()
	isResourceRemoved        = true
	startedWebhookController = false
	webhookReconciler        *WebhookReconciler
)

type WebhookReconciler struct {
	ctrl.Manager
	c client.Client
}

func NewWebhookReconciler(mgr ctrl.Manager,
) *WebhookReconciler {
	return &WebhookReconciler{
		c: mgr.GetClient(),
	}
}

func StartController(opts config.ControllerOption) (config.ControllerInterface, error) {
	if webhookReconciler != nil {
		return webhookReconciler, nil
	}
	webhookReconciler = &WebhookReconciler{
		c: opts.Manager.GetClient(),
	}
	if err := webhookReconciler.SetupWithManager(opts.Manager); err != nil {
		webhookReconciler = nil
		return nil, err
	}
	log.Infof("inited webhook controller")
	return webhookReconciler, nil
}

func (r *WebhookReconciler) IsResourceRemoved() bool {
	log.Infof("WebhookController resource removed: %v", isResourceRemoved)
	return isResourceRemoved
}

func (r *WebhookReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	mgh, err := config.GetMulticlusterGlobalHub(ctx, r.c)
	if err != nil {
		return ctrl.Result{}, err
	}
	if mgh == nil || config.IsPaused(mgh) {
		return ctrl.Result{}, nil
	}
	if mgh.DeletionTimestamp != nil || !config.GetImportClusterInHosted() {
		err = r.pruneWebhookResources(ctx)
		if err != nil {
			return ctrl.Result{}, err
		}
		isResourceRemoved = true
		return ctrl.Result{}, nil
	}
	isResourceRemoved = false
	// create new HoHRenderer and HoHDeployer
	hohRenderer, hohDeployer := renderer.NewHoHRenderer(fs), deployer.NewHoHDeployer(r.c)

	// create discovery client
	dc, err := discovery.NewDiscoveryClientForConfig(r.Manager.GetConfig())
	if err != nil {
		return ctrl.Result{}, err
	}

	// create restmapper for deployer to find GVR
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))

	webhookObjects, err := hohRenderer.Render("manifests", "", func(profile string) (interface{}, error) {
		return WebhookVariables{
			ImportClusterInHosted: config.GetImportClusterInHosted(),
			Namespace:             mgh.Namespace,
		}, nil
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to render webhook objects: %v", err)
	}
	if err = utils.ManipulateGlobalHubObjects(webhookObjects, mgh, hohDeployer, mapper, r.GetScheme()); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create/update webhook objects: %v", err)
	}
	return ctrl.Result{}, nil
}

type WebhookVariables struct {
	ImportClusterInHosted bool
	Namespace             string
}

// SetupWithManager sets up the controller with the Manager.
func (r *WebhookReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := controller.New("webhook-controller", mgr, controller.Options{
		Reconciler: r,
	})
	if err != nil {
		return err
	}
	return c.Watch(source.Kind(mgr.GetCache(), &globalhubv1alpha4.MulticlusterGlobalHub{},
		&handler.TypedEnqueueRequestForObject[*globalhubv1alpha4.MulticlusterGlobalHub]{},
		predicate.TypedFuncs[*globalhubv1alpha4.MulticlusterGlobalHub]{
			CreateFunc: func(e event.TypedCreateEvent[*globalhubv1alpha4.MulticlusterGlobalHub]) bool {
				return true
			},
			UpdateFunc: func(e event.TypedUpdateEvent[*globalhubv1alpha4.MulticlusterGlobalHub]) bool {
				if e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() {
					return true
				}
				return !reflect.DeepEqual(e.ObjectOld.GetAnnotations(), e.ObjectNew.GetAnnotations())
			},
			DeleteFunc: func(e event.TypedDeleteEvent[*globalhubv1alpha4.MulticlusterGlobalHub]) bool {
				return false
			},
		},
	))
}

func (r *WebhookReconciler) pruneWebhookResources(ctx context.Context) error {
	listOpts := []client.ListOption{
		client.MatchingLabels(map[string]string{
			constants.GlobalHubOwnerLabelKey: constants.GHOperatorOwnerLabelVal,
		}),
	}
	webhookList := &admissionregistrationv1.MutatingWebhookConfigurationList{}
	if err := r.c.List(ctx, webhookList, listOpts...); err != nil {
		return err
	}

	for idx := range webhookList.Items {
		if err := r.c.Delete(ctx, &webhookList.Items[idx]); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}

	webhookServiceListOpts := []client.ListOption{
		client.MatchingLabels(map[string]string{
			constants.GlobalHubOwnerLabelKey: constants.GHOperatorOwnerLabelVal,
			"service":                        "multicluster-global-hub-webhook",
		}),
	}
	webhookServiceList := &corev1.ServiceList{}
	if err := r.c.List(ctx, webhookServiceList, webhookServiceListOpts...); err != nil {
		return err
	}
	for idx := range webhookServiceList.Items {
		if err := r.c.Delete(ctx, &webhookServiceList.Items[idx]); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}