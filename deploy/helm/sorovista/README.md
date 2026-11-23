# SoroVista Helm chart

Deploys SoroVista in either standalone (`sourceMode: rpc`) or upstream
(`sourceMode: sorotrail`) mode. See `docs/operations/deployment.md` for
installation instructions and the values table.

```sh
helm install sorovista deploy/helm/sorovista \
  --set database.url='postgres://user:pass@postgres:5432/sorovista?sslmode=disable'
```

The chart is versioned independently of SoroVista. `appVersion` is the
default image tag; no container image is published yet, so override
`image.repository`/`image.tag` until the release pipeline exists.
