# Captured roles

The rules TestPersonas weighs: view, edit and admin as aggregated on each platform, and the roles each binds to `system:authenticated` that dotvirt's reads touch.
Captured with a read-only credential, one file per ClusterRole, `:` in a name written `_`.

- `openshift/`: hetznet, OpenShift 4.22, 2026-11-10.
- `microshift/`: da2, epheo/microshift, 2026-11-10.

Re-capture one role, rules only, one rule per line:

```sh
oc --context <ctx> get clusterrole <name> -o json | jq -c '{name: .metadata.name, rules: [.rules[] | del(..|nulls)]}'
```

then split the rules array one per line, as the files here are.
The MicroShift lane (hack/e2e-microshift.sh) proves the same answers against a real apiserver.
