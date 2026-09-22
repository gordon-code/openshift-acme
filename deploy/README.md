# Deploying the controller

Controller and exposer images are published to `ghcr.io/gordon-code/openshift-acme-controller` and `ghcr.io/gordon-code/openshift-acme-exposer`. All manifests below are restricted-v2 (Pod Security Admission) compliant.

**Before deploying:** `ghcr.io/gordon-code` packages default to **private** on first publish. Until that's changed, `oc apply` will succeed but the pod(s) will sit in `ImagePullBackOff` (the controller image on startup, and the exposer image the first time a challenge fires and the controller creates its pod). Either:
- make both `openshift-acme-controller` and `openshift-acme-exposer` packages public in the `gordon-code` org (one-time, simplest for a public fork), or
- create an `imagePullSecret` in the deployment namespace (pointing at a token with `read:packages` on those two packages) and reference it from the Deployment's `spec.template.spec.imagePullSecrets` — the exposer pod is also built from that same namespace's default `ServiceAccount`, so an image pull secret attached to that ServiceAccount covers both.

**Recommended mode:** deploy in **specific-namespaces** mode — it's the only mode that has been live-verified against a real OpenShift 4.22 cluster (issuance + re-issuance exercised end-to-end). **Single-namespace** and **cluster-wide** manifests carry the same 4.22 changes but have only been validated offline (manifest schema + cross-mode consistency checks in `make verify-deploy-consistency`) — treat them as expected-to-work, not live-confirmed. specific-namespaces is also the least-privilege choice among the three (see the RBAC note below), so it's the right default for staging evaluation as well as production.

## Issuers
Let's encrypt provides two environments: **live** and **staging**. The environment is chosen by creating the appropriate issuer. 

### Staging
*Staging* is meant for testing the controller or making sure you can try it out without the fear or exhausting your rate limits while trying it out and it will provide you with certificates signed by Let's Encrypt staging CA, making the certs **not trusted**!

### Live
*Live* will provide you with trusted certificates signed by Let's Encrypt CA but has lower rate limits. This is what you want when you're done testing/evaluating the controller.

## Deployment types

### Cluster wide
This deployment will provide certificate management for all namespaces in your cluster. You need elevated (admin) privileges to deploy it.

**RBAC note:** the `ClusterRole` grants unscoped full CRUD (`create/get/list/watch/update/patch/delete`) on `secrets` and `configmaps` across every namespace in the cluster — this is the broadest-blast-radius option of the three modes and is **not** least-privileged (see [Least-privilege framing](#least-privilege-framing) below). Prefer specific-namespaces unless you genuinely need cluster-wide certificate management.

If you have this repository checked out, deploy it like: 

```bash
oc apply -fdeploy/cluster-wide/{clusterrole,serviceaccount,issuer-letsencrypt-live,deployment}.yaml
oc create clusterrolebinding openshift-acme --clusterrole=openshift-acme --serviceaccount="$( oc project -q ):openshift-acme" --dry-run -o yaml | oc apply -f -
```

If you want to deploy it directly from GitHub use:

```bash
oc apply -fhttps://raw.githubusercontent.com/gordon-code/openshift-acme/main/deploy/cluster-wide/{clusterrole,serviceaccount,issuer-letsencrypt-live,deployment}.yaml
oc create clusterrolebinding openshift-acme --clusterrole=openshift-acme --serviceaccount="$( oc project -q ):openshift-acme" --dry-run -o yaml | oc apply -f -
```


### Single namespace
This deployment will provide certificate management for the namespace it's deployed to. You have to make sure to give the SA correct permissions but you don't have to be cluster-admin. It works fine with regular user privileges.

**RBAC note:** the `Role` grants full CRUD on `secrets` and `configmaps`, but scoped to this one namespace — see [Least-privilege framing](#least-privilege-framing) below.

If you have this repository checked out, deploy it like: 

```bash
oc apply -fdeploy/single-namespace/{role,serviceaccount,issuer-letsencrypt-live,deployment}.yaml
oc create rolebinding openshift-acme --role=openshift-acme --serviceaccount="$( oc project -q ):openshift-acme" --dry-run -o yaml | oc apply -f -
```

If you want to deploy it directly from GitHub use:

```bash
oc apply -fhttps://raw.githubusercontent.com/gordon-code/openshift-acme/main/deploy/single-namespace/{role,serviceaccount,issuer-letsencrypt-live,deployment}.yaml
oc create rolebinding openshift-acme --role=openshift-acme --serviceaccount="$( oc project -q ):openshift-acme" --dry-run -o yaml | oc apply -f -
```

### Specific namespaces
This deployment will provide certificate management for the namespace it's deployed to and explicitly specified namespaces. You have to make sure to give the SA correct permissions but you don't have to be cluster-admin. It works fine with regular user privileges. This is the recommended mode (see above) and the only one live-verified on OpenShift 4.22.

By default the shipped `deployment.yaml` only watches its own namespace (via `--namespace=$(CURRENT_NAMESPACE)`). To watch more namespaces, edit the deployment to add an extra `--namespace=test` flag per additional namespace, and give the openshift-acme serviceaccount appropriate privileges for each extra namespace, like in the example bellow for the extra namespace `test`.

**RBAC note:** the `Role` grants full CRUD on `secrets` and `configmaps`, scoped per-namespace to only the namespaces you bind it in — see [Least-privilege framing](#least-privilege-framing) below.

**Important — the controller always watches its own namespace too.** Regardless of which `--namespace` flags you pass, the controller unconditionally adds its own namespace to the watch set. That means the `openshift-acme` workload `Role` (and the separate leader-election `Role`) **must be bound in the controller's own namespace**, not only in the target namespaces you're adding. If you skip the controller-namespace binding, the controller pod comes up `Running` and even acquires the leader-election lease, but its informers fail `LIST`/`WATCH` as forbidden and it silently never reconciles anything — there's no crash or restart to signal the problem.

Leader election also needs its own `Role` (`openshift-acme-leaderelection`, granting `leases` in `coordination.k8s.io`) bound **only** in the controller's own namespace — never in the additional watched namespaces, since the Lease itself only ever lives in the controller's own namespace.

If you have this repository checked out, deploy it like (example: controller running in `openshift-acme`, additionally watching `test`):

```bash
# The shipped deployment.yaml only watches its own namespace by default (--namespace=$(CURRENT_NAMESPACE)).
# To additionally watch `test`, edit deploy/specific-namespaces/deployment.yaml first and add an
# extra --namespace=test arg (alongside the existing one) for each namespace you want to watch.
oc apply -fdeploy/specific-namespaces/{role,role-leaderelection,serviceaccount,issuer-letsencrypt-live,deployment}.yaml

# Bind the workload role in the controller's OWN namespace (always required, even with no extra --namespace flags).
oc create rolebinding openshift-acme --role=openshift-acme --serviceaccount="$( oc project -q ):openshift-acme" -n "$( oc project -q )"

# Bind the leader-election role, ALSO in the controller's own namespace only.
oc create rolebinding openshift-acme-leaderelection --role=openshift-acme-leaderelection --serviceaccount="$( oc project -q ):openshift-acme" -n "$( oc project -q )"

# Bind the workload role in each additional watched namespace (repeat per --namespace flag).
oc create rolebinding openshift-acme --role=openshift-acme --serviceaccount="$( oc project -q ):openshift-acme" -n "test"
```

If you want to deploy it directly from GitHub use:

```bash
# Applied straight from GitHub, the controller watches only its own namespace.
# To watch additional namespaces, use the checked-out path above so you can edit
# deployment.yaml to add extra --namespace flags before applying.
oc apply -fhttps://raw.githubusercontent.com/gordon-code/openshift-acme/main/deploy/specific-namespaces/{role,role-leaderelection,serviceaccount,issuer-letsencrypt-live,deployment}.yaml
oc create rolebinding openshift-acme --role=openshift-acme --serviceaccount="$( oc project -q ):openshift-acme" -n "$( oc project -q )"
oc create rolebinding openshift-acme-leaderelection --role=openshift-acme-leaderelection --serviceaccount="$( oc project -q ):openshift-acme" -n "$( oc project -q )"
```

## Least-privilege framing

The RBAC in all three modes is **namespace-scoped, not resource-level least-privileged**. Every mode grants unscoped full CRUD (`create/get/list/watch/update/patch/delete`) on `secrets` and `configmaps` within whatever namespace(s) the Role/ClusterRole applies to — some of those verbs (e.g. `delete`, `watch` on `configmaps`) are broader than what the controller code actually exercises for a given resource. specific-namespaces and single-namespace bound this to one or a handful of namespaces you control; cluster-wide grants it across every namespace in the cluster and is the largest blast radius of the three. Don't describe any of these modes as "least-privileged" without that qualifier — the real privilege boundary here is the namespace, not the resource or verb.
