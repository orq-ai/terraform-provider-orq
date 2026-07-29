# dev-playground

A throwaway directory for authoring orq Terraform snippets **with IDE completions**
before the provider is published. Not part of the published examples.

```sh
make ide-install          # from repo root: build into the local OpenTofu mirror
cd dev-playground && tofu init
```

See `../DEVELOPMENT.md` for details. `.terraform/`, lock files and state here are
git-ignored; `main.tf` is a starting point — edit freely.
