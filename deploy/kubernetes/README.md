# Kubernetes example

Edit `configmap.yaml`, then create the token Secret without storing the token in Git:

```sh
kubectl create secret generic hetdns-token --from-literal=token='YOUR_TOKEN'
kubectl apply -k deploy/kubernetes
kubectl port-forward service/hetdns 8080:8080
```

`secret.example.yaml` documents the required Secret shape but is intentionally not part of the
Kustomize base. Run one replica only. If exposing the Service through an Ingress, put TLS and
authentication in front of it; the UI reveals hostnames and addresses.
