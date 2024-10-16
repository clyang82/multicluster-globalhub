// Copyright (c) 2024 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	klusterletv1alpha1 "github.com/stolostron/cluster-lifecycle-api/klusterletconfig/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	operatorv1 "open-cluster-management.io/api/operator/v1"
	"open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	migrationv1alpha1 "github.com/stolostron/multicluster-global-hub/operator/api/migration/v1alpha1"
	bundleevent "github.com/stolostron/multicluster-global-hub/pkg/bundle/event"
	"github.com/stolostron/multicluster-global-hub/pkg/constants"
	"github.com/stolostron/multicluster-global-hub/pkg/database"
	"github.com/stolostron/multicluster-global-hub/pkg/transport"
	"github.com/stolostron/multicluster-global-hub/pkg/utils"
)

// MigrationReconciler reconciles a ManagedClusterMigration object
type MigrationReconciler struct {
	client.Client
	transport.Producer
	Migrations            map[string]map[string](bundleevent.ManagedClusterMigrationFromEvent)
	importClusterInHosted bool
}

func NewMigrationReconciler(client client.Client, producer transport.Producer,
	importClusterInHosted bool,
) *MigrationReconciler {
	return &MigrationReconciler{
		Client:                client,
		Producer:              producer,
		importClusterInHosted: importClusterInHosted,
		Migrations:            map[string]map[string](bundleevent.ManagedClusterMigrationFromEvent){},
	}
}

const (
	klusterletConfigNamePrefix = "migration-"
	bootstrapSecretNamePrefix  = "bootstrap-"
)

// SetupWithManager sets up the controller with the Manager.
func (m *MigrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("migration-controller").
		For(&migrationv1alpha1.ManagedClusterMigration{}).
		Watches(&v1beta1.ManagedServiceAccount{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				if !obj.GetDeletionTimestamp().IsZero() {
					// trigger to recreate the msa
					return []reconcile.Request{
						{
							NamespacedName: types.NamespacedName{
								Name:      obj.GetName(),
								Namespace: utils.GetDefaultNamespace(),
							},
						},
					}
				}
				return []reconcile.Request{
					{
						NamespacedName: types.NamespacedName{
							Name:      obj.GetName(),
							Namespace: obj.GetNamespace(),
						},
					},
				}
			}),
			builder.WithPredicates(predicate.Funcs{
				CreateFunc: func(e event.CreateEvent) bool {
					return false
				},
				UpdateFunc: func(e event.UpdateEvent) bool {
					labels := e.ObjectNew.GetLabels()
					if value, ok := labels["owner"]; ok {
						if value == strings.ToLower(constants.ManagedClusterMigrationKind) {
							return e.ObjectOld.GetResourceVersion() != e.ObjectNew.GetResourceVersion()
						}
						return false
					}
					return false
				},
				DeleteFunc: func(e event.DeleteEvent) bool {
					e.Object.SetDeletionTimestamp(&metav1.Time{Time: time.Now()})
					labels := e.Object.GetLabels()
					if value, ok := labels["owner"]; ok {
						if value == strings.ToLower(constants.ManagedClusterMigrationKind) {
							return !e.DeleteStateUnknown
						}
						return false
					}
					return false
				},
			})).
		Complete(m)
}

func (m *MigrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	if req.Namespace == utils.GetDefaultNamespace() {
		// create managedserviceaccount
		migration := &migrationv1alpha1.ManagedClusterMigration{}
		err := m.Get(ctx, req.NamespacedName, migration)
		if err != nil {
			if apierrors.IsNotFound(err) {
				// If the custom resource is not found then it usually means that it was deleted or not created
				// In this way, we will stop the reconciliation
				log.Info("managedclustermigration resource not found. Ignoring since object must be deleted")
				return ctrl.Result{}, nil
			}
			// Error reading the object - requeue the request.
			log.Error(err, "failed to get managedclustermigration")
			return ctrl.Result{}, err
		}

		if migration.DeletionTimestamp.IsZero() {
			if !controllerutil.ContainsFinalizer(migration, constants.ManagedClusterMigrationFinalizer) {
				controllerutil.AddFinalizer(migration, constants.ManagedClusterMigrationFinalizer)
				return ctrl.Result{}, m.Update(ctx, migration)
			}
		} else {
			// The migration object is being deleted
			if controllerutil.ContainsFinalizer(migration, constants.ManagedClusterMigrationFinalizer) {
				if err := m.deleteManagedServiceAccount(ctx, migration); err != nil { // TODO: delete all msa
					return ctrl.Result{}, err
				}

				controllerutil.RemoveFinalizer(migration, constants.ManagedClusterMigrationFinalizer)
				if err := m.Update(ctx, migration); err != nil {
					return ctrl.Result{}, err
				}
			}
			delete(m.Migrations, migration.Name)
			return ctrl.Result{}, nil
		}
		// should be handled in server foundation. provide the default kubeconfig for the exixting hub
		managedClusterMap, err := m.fillInManagedClusterMap(migration)
		if err != nil {
			return ctrl.Result{}, err
		}
		for leafHubName := range managedClusterMap {
			if err := m.ensureManagedServiceAccount(ctx, migration.GetName(), leafHubName); err != nil {
				return ctrl.Result{}, err
			}
		}

		if err := m.ensureManagedServiceAccount(ctx, migration.GetName(), migration.Spec.To); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		migration := &migrationv1alpha1.ManagedClusterMigration{}
		if err := m.Get(ctx, types.NamespacedName{
			Name:      req.Name,
			Namespace: utils.GetDefaultNamespace(),
		}, migration); err != nil {
			log.Error(err, "failed to get managedclustermigration")
			return ctrl.Result{}, err
		}
		if _, exists := m.Migrations[migration.Name]; !exists {
			m.Migrations[migration.Name] = map[string](bundleevent.ManagedClusterMigrationFromEvent){}
			managedClusterMap, err := m.fillInManagedClusterMap(migration)
			if err != nil {
				return ctrl.Result{}, err
			}
			for leafHubName, managedClusters := range managedClusterMap {
				m.Migrations[migration.Name][leafHubName] = bundleevent.ManagedClusterMigrationFromEvent{
					ManagedClusters: managedClusters,
				}
			}
		}
		// check if the secret is created by managedserviceaccount, if not, requeue after 1 second
		desiredSecret := &corev1.Secret{}
		if err := m.Client.Get(ctx, types.NamespacedName{
			Name:      req.Name,
			Namespace: req.Namespace,
		}, desiredSecret); err != nil {
			if apierrors.IsNotFound(err) {
				return ctrl.Result{Requeue: true, RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, err
		}

		// create kubeconfig based on the secret of managedserviceaccount
		kubeconfig, err := m.generateKubeconfig(ctx, req)
		if err != nil {
			return ctrl.Result{}, err
		}

		bootstrapSecret, err := m.generateBootstrapSecret(kubeconfig, req)
		if err != nil {
			return ctrl.Result{}, err
		}

		if req.Namespace == migration.Spec.To {
			for key := range m.Migrations[migration.Name] {
				evt := m.Migrations[migration.Name][key]
				evt.BootstrapSecret = bootstrapSecret
				m.Migrations[migration.Name][key] = evt
			}
		} else {
			evt := m.Migrations[migration.Name][req.Namespace]
			evt.OriginalBootstrapSecret = bootstrapSecret
			m.Migrations[migration.Name][req.Namespace] = evt
		}
		for _, evt := range m.Migrations[migration.Name] {
			if evt.BootstrapSecret != nil && evt.OriginalBootstrapSecret != nil {
				evt.KlusterletConfig = m.generateKlusterConfig(migration, evt)
				// send the kubeconfig to managedclustermigration.Spec.From
				if err := m.syncMigration(ctx, migration, evt); err != nil {
					return ctrl.Result{}, err
				}
			}
		}
	}
	return ctrl.Result{}, nil
}

func (m *MigrationReconciler) generateKlusterConfig(migration *migrationv1alpha1.ManagedClusterMigration,
	event bundleevent.ManagedClusterMigrationFromEvent) *klusterletv1alpha1.KlusterletConfig {
	return &klusterletv1alpha1.KlusterletConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: klusterletConfigNamePrefix + migration.Spec.To,
		},
		Spec: klusterletv1alpha1.KlusterletConfigSpec{
			BootstrapKubeConfigs: operatorv1.BootstrapKubeConfigs{
				Type: operatorv1.LocalSecrets,
				LocalSecrets: operatorv1.LocalSecretsConfig{
					KubeConfigSecrets: []operatorv1.KubeConfigSecret{
						{
							Name: event.BootstrapSecret.Name,
						},
						{
							Name: event.OriginalBootstrapSecret.Name,
						},
					},
				},
			},
		},
	}
}

func (m *MigrationReconciler) generateBootstrapSecret(kubeconfig *clientcmdapi.Config,
	req ctrl.Request,
) (*corev1.Secret, error) {
	// Serialize the kubeconfig to YAML
	kubeconfigBytes, err := clientcmd.Write(*kubeconfig)
	if err != nil {
		return nil, err
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bootstrapSecretNamePrefix + req.Namespace,
			Namespace: "multicluster-engine",
		},
		Data: map[string][]byte{
			"kubeconfig": kubeconfigBytes,
		},
	}, nil
}

func (m *MigrationReconciler) generateKubeconfig(ctx context.Context, req ctrl.Request) (*clientcmdapi.Config, error) {
	// get the secret which is generated by msa
	desiredSecret := &corev1.Secret{}
	if err := m.Client.Get(ctx, types.NamespacedName{
		Name:      req.Name,
		Namespace: req.Namespace,
	}, desiredSecret); err != nil {
		return nil, err
	}
	// fetch the managed cluster to get url
	managedcluster := &clusterv1.ManagedCluster{}
	if err := m.Client.Get(ctx, types.NamespacedName{
		Name: req.Namespace,
	}, managedcluster); err != nil {
		return nil, err
	}

	config := clientcmdapi.NewConfig()
	config.Clusters[req.Namespace] = &clientcmdapi.Cluster{
		Server:                   managedcluster.Spec.ManagedClusterClientConfigs[0].URL,
		CertificateAuthorityData: desiredSecret.Data["ca.crt"],
	}
	config.AuthInfos["user"] = &clientcmdapi.AuthInfo{
		Token: string(desiredSecret.Data["token"]),
	}
	config.Contexts["default-context"] = &clientcmdapi.Context{
		Cluster:  req.Namespace,
		AuthInfo: "user",
	}
	config.CurrentContext = "default-context"

	return config, nil
}

func (m *MigrationReconciler) ensureManagedServiceAccount(ctx context.Context,
	msaName, msaNamespace string,
) error {
	// create a desired msa
	desiredMSA := &v1beta1.ManagedServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      msaName,
			Namespace: msaNamespace,
			Labels: map[string]string{
				"owner": strings.ToLower(constants.ManagedClusterMigrationKind),
			},
		},
		Spec: v1beta1.ManagedServiceAccountSpec{
			Rotation: v1beta1.ManagedServiceAccountRotation{
				Enabled: true,
				Validity: metav1.Duration{
					Duration: 86400 * time.Hour,
				},
			},
		},
	}

	existingMSA := &v1beta1.ManagedServiceAccount{}
	err := m.Client.Get(ctx, types.NamespacedName{
		Name:      msaName,
		Namespace: msaNamespace,
	}, existingMSA)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return m.Client.Create(ctx, desiredMSA)
		}
		return err
	}
	return nil
}

func (m *MigrationReconciler) deleteManagedServiceAccount(ctx context.Context,
	migration *migrationv1alpha1.ManagedClusterMigration,
) error {
	// enhance to continue deleting others if one is failed
	for key := range m.Migrations[migration.Name] {
		msa := &v1beta1.ManagedServiceAccount{}
		if err := m.Get(ctx, types.NamespacedName{
			Name:      migration.Name,
			Namespace: key,
		}, msa); err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
		}
		if err := m.Delete(ctx, msa); err != nil {
			return err
		}
	}

	// delete To managedserviceaccount
	msa := &v1beta1.ManagedServiceAccount{}
	if err := m.Get(ctx, types.NamespacedName{
		Name:      migration.Name,
		Namespace: migration.Spec.To,
	}, msa); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
	}
	return m.Delete(ctx, msa)
}

func (m *MigrationReconciler) fillInManagedClusterMap(migration *migrationv1alpha1.ManagedClusterMigration) (
	map[string][]string, error) {
	managedClusterMap := make(map[string][]string)
	if migration.Spec.From != "" {
		managedClusterMap[migration.Spec.From] = migration.Spec.IncludedManagedClusters
		return managedClusterMap, nil
	}

	db := database.GetGorm()
	rows, err := db.Raw(`SELECT leaf_hub_name, cluster_name FROM status.managed_clusters
			WHERE cluster_name IN (?)`,
		migration.Spec.IncludedManagedClusters).Rows()
	if err != nil {
		return nil, fmt.Errorf("failed to get leaf hub name and managed clusters - %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var leafHubName, managedClusterName string
		if err := rows.Scan(&leafHubName, &managedClusterName); err != nil {
			return nil, fmt.Errorf("failed to scan leaf hub name and managed cluster name - %w", err)
		}
		managedClusterMap[leafHubName] = append(managedClusterMap[leafHubName], managedClusterName)
	}
	return managedClusterMap, nil
}

func (m *MigrationReconciler) syncMigration(ctx context.Context,
	migration *migrationv1alpha1.ManagedClusterMigration,
	event bundleevent.ManagedClusterMigrationFromEvent,
) error {
	managedClusterMap, err := m.fillInManagedClusterMap(migration)
	if err != nil {
		return err
	}
	// send the migration event to migration.from managed hub(s)
	for leafHubName := range managedClusterMap {
		if err := m.syncMigrationFrom(ctx, leafHubName, event); err != nil {
			return err
		}
	}

	// send the migration event to migration.to managed hub
	return m.syncMigrationTo(ctx, migration)
}

func (m *MigrationReconciler) syncMigrationFrom(ctx context.Context, fromHub string,
	managedClusterMigrationFromEvent bundleevent.ManagedClusterMigrationFromEvent,
) error {
	payloadBytes, err := json.Marshal(managedClusterMigrationFromEvent)
	if err != nil {
		return fmt.Errorf("failed to marshal managed cluster migration from event(%v) - %w",
			managedClusterMigrationFromEvent, err)
	}

	eventType := constants.CloudEventTypeMigrationFrom
	evt := utils.ToCloudEvent(eventType, constants.CloudEventSourceGlobalHub, fromHub, payloadBytes)
	if err := m.Producer.SendEvent(ctx, evt); err != nil {
		return fmt.Errorf("failed to sync managedclustermigration event(%s) from source(%s) to destination(%s) - %w",
			eventType, constants.CloudEventSourceGlobalHub, fromHub, err)
	}

	return nil
}

func (m *MigrationReconciler) syncMigrationTo(ctx context.Context,
	migration *migrationv1alpha1.ManagedClusterMigration,
) error {
	// default managedserviceaccount addon namespace
	msaNamespace := "open-cluster-management-agent-addon"
	if m.importClusterInHosted {
		// hosted mode, the  managedserviceaccount addon namespace
		msaNamespace = "open-cluster-management-global-hub-agent-addon"
	}
	msaInstallNamespaceAnnotation := "global-hub.open-cluster-management.io/managed-serviceaccount-install-namespace"
	// if user specifies the managedserviceaccount addon namespace, then use it
	if val, ok := migration.Annotations[msaInstallNamespaceAnnotation]; ok {
		msaNamespace = val
	}
	managedClusterMigrationToEvent := &bundleevent.ManagedClusterMigrationToEvent{
		ManagedServiceAccountName:             migration.Name,
		ManagedServiceAccountInstallNamespace: msaNamespace,
	}
	payloadToBytes, err := json.Marshal(managedClusterMigrationToEvent)
	if err != nil {
		return fmt.Errorf("failed to marshal managed cluster migration to event(%v) - %w",
			managedClusterMigrationToEvent, err)
	}

	// send the event to the destination managed hub
	eventType := constants.CloudEventTypeMigrationTo
	evt := utils.ToCloudEvent(eventType, constants.CloudEventSourceGlobalHub, migration.Spec.To, payloadToBytes)
	if err := m.Producer.SendEvent(ctx, evt); err != nil {
		return fmt.Errorf("failed to sync managedclustermigration event(%s) from source(%s) to destination(%s) - %w",
			eventType, constants.CloudEventSourceGlobalHub, migration.Spec.To, err)
	}

	return nil
}
