# The Hub-of-Hubs multiple-desktop basic-demo script

1.  Login into the Web console of `hub1`. As the `hub1` user, observe managed clusters `cluster0` to `cluster4` in the
    Cluster view.

1.  In the terminal of the `hub1` user, run:

    ```
    kubectl get managedcluster
    ```

    You should see clusters `cluster0` to `cluster4` returned.

    ![Screenshot of the desktop of the hub1 user, Cluster view](images/hub1.png)

1.  Perform the previous two steps as the user of `hub2`.

    ![Screenshot of the desktop of the hub2 user, Cluster view](images/hub2.png)

1.  Login into the Web console of Hub of Hubs as the `kubeadmin` user. The Hub-of-Hubs Web console has the same URL as the original [ACM Web console URL](https://access.redhat.com/documentation/en-us/red_hat_advanced_cluster_management_for_kubernetes/2.4/html/web_console/web-console#accessing-your-console).

    If you cannot login as `kubeadmin`, [add an alternative user as the admin to Hub-of-Hubs RBAC](https://github.com/stolostron/hub-of-hubs-rbac#update-role-bindings-or-role-definitions).

1.  Note that the managed clusters on `hoh` are not represented by Kubernetes Custom Resources (not stored in etcd),
    and cannot be queried by `kubectl`:

    ```
    $ kubectl get managedcluster
    No resources found
    ```

    ![Screenshot of the desktop of the Hub-of-Hubs user, Cluster view](images/hoh.png)

1.  Browse the Web console of Hub of Hubs. Note that currently it has only three views, namely `Welcome`, `Clusters` and
    `Governance`. Also note that the Cluster view has neither tabs nor buttons to create or import a cluster.
    The cluster table does not have actions to detach a cluster or to edit cluster labels.

    ![Screenshot of the desktop of the Hub-of-Hubs user, Welcome view](images/hoh_welcome.png)

1.  Add the `env=production` label to some of the managed clusters of `hub1` and `hub2`, either by `kubectl` or
    [in the Web console](https://access.redhat.com/documentation/en-us/red_hat_advanced_cluster_management_for_kubernetes/2.4/html/clusters/managing-your-clusters#managing-cluster-labels) of `hub1`/`hub2`.

    ```
    $ kubectl label managedcluster <some-cluster> env=production --kubeconfig $HUB1_CONFIG
    ```

1.  Note that the new labels appear in the Cluster View of `hoh`.

    The screenshot below shows the previous example setup with labels `env=production` on clusters `cluster0`, `cluster3`, `cluster7`, `cluster8` and `cluster9`.
    The labels appear in the Cluster View of `hub1`, `hub2` and `hoh`.


1.  Create a policy, a placement rule and a placement binding in `hoh` cluster by `kubectl`. The policy used in the instructions below is an [ACM pod security policy](https://access.redhat.com/documentation/en-us/red_hat_advanced_cluster_management_for_kubernetes/2.4/html/governance/governance#pod-security-policy). The placement rule selects clusters with the `env=production` label.

    ```
    $ kubectl apply -f https://raw.githubusercontent.com/stolostron/hub-of-hubs/main/demos/policy-psp.yaml --kubeconfig $TOP_HUB_CONFIG
    policy.policy.open-cluster-management.io/policy-podsecuritypolicy created
    placementbinding.policy.open-cluster-management.io/binding-policy-podsecuritypolicy created
    placementrule.apps.open-cluster-management.io/placement-policy-podsecuritypolicy created
    ```

1.  Observe the policy in the Web console of `hoh`, `hub1` and `hub2`.

1.  Click on the _Cluster violantions_ link in the Governance view of `hoh`, `hub1` and `hub2`.
    Note that all the managed clusters labeled with `env=production` from both `hub1` and `hub2` appear in the Cluster violiations view of `hoh`.

1.  Change compliance of one of the managed clusters of `hub1`. To make the managed cluster compliant, run:

    ```
    $ kubectl apply -f https://raw.githubusercontent.com/stolostron/hub-of-hubs/main/demos/psp.yaml --kubeconfig <a managed cluster config>
    ```

1.  Observe changes of the compliance status in the Web console of `hoh` and `hub1`.

1.  Change the remediation action in the Web console of `hoh` to `enforce`. Observe propagation of the changes and the status.

1.  Delete the policy in the Web console of `hoh`. Observe propagation of the deletion to `hub1` and `hub2`.