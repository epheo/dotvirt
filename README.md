<p align="center">
  <img src="docs/dotvirt.png" alt="dotvirt" width="200" />
</p>

# dotvirt

A vCenter-like WebUI that closes the gap between point-and-click VM operation and
GitOps. dotvirt **edits git repos** of KubeVirt manifests and **works alongside
ArgoCD**. Argo stays the only thing that applies state to the cluster; dotvirt is
the friendly inventory + editor on top of git and Argo's status.

It is **multi-user and multi-tenant as a thin lens that owns nothing**: it rides
the cluster's own authentication and RBAC.

https://www.apache.org/licenses/LICENSE-2.0.txt
