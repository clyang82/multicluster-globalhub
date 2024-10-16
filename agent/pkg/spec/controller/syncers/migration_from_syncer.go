package syncers

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-logr/logr"
	klusterletv1alpha1 "github.com/stolostron/cluster-lifecycle-api/klusterletconfig/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	operatorv1 "open-cluster-management.io/api/operator/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bundleevent "github.com/stolostron/multicluster-global-hub/pkg/bundle/event"
	"github.com/stolostron/multicluster-global-hub/pkg/constants"
)

type managedClusterMigrationFromSyncer struct {
	log    logr.Logger
	client client.Client
}

func NewManagedClusterMigrationFromSyncer(client client.Client) *managedClusterMigrationFromSyncer {
	return &managedClusterMigrationFromSyncer{
		log:    ctrl.Log.WithName("managed-cluster-migration-from-syncer"),
		client: client,
	}
}

func (s *managedClusterMigrationFromSyncer) Sync(ctx context.Context, payload []byte) error {
	// handle migration.from cloud event
	managedClusterMigrationEvent := &bundleevent.ManagedClusterMigrationFromEvent{}
	if err := json.Unmarshal(payload, managedClusterMigrationEvent); err != nil {
		return err
	}

	// create or update bootstrap secret
	bootstrapSecret := managedClusterMigrationEvent.BootstrapSecret
	foundBootstrapSecret := &corev1.Secret{}
	if err := s.client.Get(ctx,
		types.NamespacedName{
			Name:      bootstrapSecret.Name,
			Namespace: bootstrapSecret.Namespace,
		}, foundBootstrapSecret); err != nil {
		if apierrors.IsNotFound(err) {
			s.log.Info("creating bootstrap secret", "bootstrap secret", bootstrapSecret)
			if err := s.client.Create(ctx, bootstrapSecret); err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		// update the bootstrap secret if it already exists
		s.log.Info("updating bootstrap secret", "bootstrap secret", bootstrapSecret)
		if err := s.client.Update(ctx, bootstrapSecret); err != nil {
			return err
		}
	}

	// create or update the original bootstrap secret
	originalBootstrapSecret := managedClusterMigrationEvent.OriginalBootstrapSecret
	foundOriginalBootstrapSecret := &corev1.Secret{}
	if err := s.client.Get(ctx,
		types.NamespacedName{
			Name:      originalBootstrapSecret.Name,
			Namespace: originalBootstrapSecret.Namespace,
		}, foundOriginalBootstrapSecret); err != nil {
		if apierrors.IsNotFound(err) {
			s.log.Info("creating bootstrap secret", "bootstrap secret", originalBootstrapSecret)
			if err := s.client.Create(ctx, originalBootstrapSecret); err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		// update the bootstrap secret if it already exists
		s.log.Info("updating bootstrap secret", "bootstrap secret", bootstrapSecret)
		if err := s.client.Update(ctx, originalBootstrapSecret); err != nil {
			return err
		}
	}

	// create klusterlet config if it does not exist
	klusterletConfig := managedClusterMigrationEvent.KlusterletConfig
	// set the bootstrap kubeconfig secrets in klusterlet config
	klusterletConfig.Spec.BootstrapKubeConfigs.LocalSecrets.KubeConfigSecrets = []operatorv1.KubeConfigSecret{
		{
			Name: bootstrapSecret.Name,
		},
		{
			Name: originalBootstrapSecret.Name,
		},
	}
	foundKlusterletConfig := &klusterletv1alpha1.KlusterletConfig{}
	if err := s.client.Get(ctx,
		types.NamespacedName{
			Name: klusterletConfig.Name,
		}, foundKlusterletConfig); err != nil {
		if apierrors.IsNotFound(err) {
			s.log.Info("creating klusterlet config", "klusterlet config", klusterletConfig)
			if err := s.client.Create(ctx, klusterletConfig); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	if !apiequality.Semantic.DeepDerivative(klusterletConfig.Spec, foundKlusterletConfig.Spec) {
		foundKlusterletConfig.Spec = klusterletConfig.Spec
		s.log.Info("updating klusterlet config", "klusterlet config", foundKlusterletConfig)
		if err := s.client.Update(ctx, foundKlusterletConfig); err != nil {
			return err
		}
	}

	// update managed cluster annotations to point to the new klusterlet config
	managedClusters := managedClusterMigrationEvent.ManagedClusters
	for _, managedCluster := range managedClusters {
		mcl := &clusterv1.ManagedCluster{}
		if err := s.client.Get(ctx, types.NamespacedName{
			Name: managedCluster,
		}, mcl); err != nil {
			return err
		}
		annotations := mcl.Annotations
		if annotations == nil {
			annotations = make(map[string]string)
		}

		_, migrating := annotations[constants.ManagedClusterMigrating]
		_, disableAutoImport := annotations["import.open-cluster-management.io/disable-auto-import"]
		if migrating && disableAutoImport && annotations["agent.open-cluster-management.io/klusterlet-config"] == klusterletConfig.Name {
			continue
		}
		annotations["import.open-cluster-management.io/disable-auto-import"] = ""
		annotations["agent.open-cluster-management.io/klusterlet-config"] = klusterletConfig.Name
		annotations[constants.ManagedClusterMigrating] = ""
		mcl.SetAnnotations(annotations)
		s.log.Info("updating managed cluster to patch klusterlet-config")
		if err := s.client.Update(ctx, mcl); err != nil {
			return err
		}
	}

	// wait for 10 seconds to ensure the klusterletconfig is applied and then trigger the migration
	time.Sleep(10 * time.Second)
	for _, managedCluster := range managedClusters {
		mcl := &clusterv1.ManagedCluster{}
		if err := s.client.Get(ctx, types.NamespacedName{
			Name: managedCluster,
		}, mcl); err != nil {
			return err
		}
		mcl.Spec.HubAcceptsClient = false
		s.log.Info("updating managed cluster to set HubAcceptsClient as false", "managed cluster", mcl.Name)
		if err := s.client.Update(ctx, mcl); err != nil {
			return err
		}
	}

	// check managed cluster available unknown status and detach the managed cluster in new go routine
	if err := s.detachManagedClusters(ctx, managedClusters); err != nil {
		s.log.Error(err, "failed to detach managed clusters")
	}

	return nil
}

func (s *managedClusterMigrationFromSyncer) detachManagedClusters(ctx context.Context, managedClusters []string) error {
	return wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		for _, managedCluster := range managedClusters {
			mcl := &clusterv1.ManagedCluster{}
			if err := s.client.Get(ctx, types.NamespacedName{
				Name: managedCluster,
			}, mcl); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				} else {
					return false, err
				}
			}
			if meta.IsStatusConditionPresentAndEqual(mcl.Status.Conditions,
				clusterv1.ManagedClusterConditionAvailable, metav1.ConditionUnknown) {
				if err := s.client.Delete(ctx, mcl); err != nil {
					return false, err
				}
			} else {
				return false, nil
			}
		}
		return true, nil
	})
}
