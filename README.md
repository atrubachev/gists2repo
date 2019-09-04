# A simple tool to convert all gists to a git repo

Usage example
```
$  (rm -rf algorithms/; mkdir algorithms/ && cd algorithms/ && git init)
$  go run cmd/gist2repo/main.go --token=<oauth token> --user=<gist user> --repo=$(pwd)/algorithms
```