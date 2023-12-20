// Copyright (c) 2022 Red Hat(scheme)) Inc.
// Copyright Contributors to the Open Cluster Management project

package scheme

import (
	routev1 "github.com/openshift/api/route/v1"
	mchv1 "github.com/stolostron/multiclusterhub-operator/api/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	clusterv1alpha1 "open-cluster-management.io/api/cluster/v1alpha1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	clusterv1beta2 "open-cluster-management.io/api/cluster/v1beta2"
	operatorv1 "open-cluster-management.io/api/operator/v1"
	policyv1 "open-cluster-management.io/governance-policy-propagator/api/v1"
	channelv1 "open-cluster-management.io/multicloud-operators-channel/pkg/apis/apps/v1"
	placementrulev1 "open-cluster-management.io/multicloud-operators-subscription/pkg/apis/apps/placementrule/v1"
	appsubv1 "open-cluster-management.io/multicloud-operators-subscription/pkg/apis/apps/v1"
	appsubv1alpha1 "open-cluster-management.io/multicloud-operators-subscription/pkg/apis/apps/v1alpha1"
	appv1beta1 "sigs.k8s.io/application/api/v1beta1"
)

// AddToScheme adds all the resources to be processed to the Scheme.
func AddToScheme(scheme *runtime.Scheme) {
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(clusterv1alpha1.AddToScheme(scheme))
	utilruntime.Must(clusterv1beta1.AddToScheme(scheme))
	utilruntime.Must(clusterv1beta2.AddToScheme(scheme))
	utilruntime.Must(operatorv1.AddToScheme(scheme))
	utilruntime.Must(apiregistrationv1.AddToScheme(scheme))
	utilruntime.Must(routev1.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(coordinationv1.AddToScheme(scheme))
	utilruntime.Must(mchv1.AddToScheme(scheme))
	utilruntime.Must(policyv1.AddToScheme(scheme))
	utilruntime.Must(placementrulev1.AddToScheme(scheme))
	utilruntime.Must(appsubv1alpha1.AddToScheme(scheme))
	utilruntime.Must(channelv1.AddToScheme(scheme))
	utilruntime.Must(appsubv1.SchemeBuilder.AddToScheme(scheme))
	utilruntime.Must(appv1beta1.AddToScheme(scheme))
}
