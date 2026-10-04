package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	appsv1 "k8s.io/api/apps/v1"
)

func main() {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
	if err != nil || len(raw) == 0 || len(raw) >= 8<<20 {
		panic("bounded deployment input required")
	}
	var deployment appsv1.Deployment
	if err = json.Unmarshal(raw, &deployment); err != nil {
		panic("invalid deployment")
	}
	encoded, err := json.Marshal(deployment.Spec)
	if err != nil {
		panic("deployment spec encoding failed")
	}
	digest := sha256.Sum256(encoded)
	fmt.Println(hex.EncodeToString(digest[:]))
}
