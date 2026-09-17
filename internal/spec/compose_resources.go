package spec

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/api/resource"
)

func (r *composeReader) resources(s *Service, node *yaml.Node, field string) error {
	if node == nil {
		return nil
	}
	fields, err := composeMap(node, field, "limits", "reservations")
	if err != nil {
		return err
	}
	for _, group := range []string{"limits", "reservations"} {
		values, err := composeMap(fields[group], field+"."+group, "cpus", "memory")
		if err != nil {
			return err
		}
		for _, key := range []string{"cpus", "memory"} {
			if values[key] == nil {
				continue
			}
			dest := strings.TrimSuffix(key, "s") + "_limit"
			if group == "reservations" {
				dest = strings.TrimSuffix(key, "s") + "_request"
			}
			if err := r.resourceValue(s, values[key], dest, key == "memory", field+"."+group+"."+key); err != nil {
				return err
			}
		}
	}
	return nil
}

var composeMemory = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)(b|k|kb|m|mb|g|gb|t|tb)?$`)

func (r *composeReader) resourceValue(s *Service, n *yaml.Node, dest string, memory bool, field string) error {
	value, err := r.text(n, field)
	if err != nil {
		return err
	}
	if memory {
		// Compose's m/mb are mebibytes; Kubernetes m means a thousandth of a byte.
		parts := composeMemory.FindStringSubmatch(value)
		if len(value) > 32 || parts == nil {
			return fmt.Errorf("%s: use bytes or a Compose byte unit, such as 512m or 1g", field)
		}
		amount, ok := new(big.Rat).SetString(parts[1])
		if !ok {
			return fmt.Errorf("%s: invalid byte quantity", field)
		}
		unit := strings.ToLower(parts[2])
		power := map[string]uint{"k": 10, "kb": 10, "m": 20, "mb": 20, "g": 30, "gb": 30, "t": 40, "tb": 40}[unit]
		amount.Mul(amount, new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), power)))
		if !amount.IsInt() || !amount.Num().IsInt64() {
			return fmt.Errorf("%s: use a whole number of bytes within supported resource bounds", field)
		}
		value = resource.NewQuantity(amount.Num().Int64(), resource.BinarySI).String()
	} else if !cpuQuantity.MatchString(value) || len(value) > 32 {
		return fmt.Errorf("%s: use CPU cores with at most three decimal places", field)
	}
	q, err := resource.ParseQuantity(value)
	if err != nil || q.Sign() <= 0 {
		return fmt.Errorf("%s: use a positive resource quantity", field)
	}
	if s.Resources == nil {
		s.Resources = &Resources{}
	}
	values := map[string]*string{"cpu_request": &s.Resources.CPURequest, "cpu_limit": &s.Resources.CPULimit, "memory_request": &s.Resources.MemoryRequest, "memory_limit": &s.Resources.MemoryLimit}
	target := values[dest]
	if *target != "" {
		previous, err := resource.ParseQuantity(*target)
		if err != nil || previous.Cmp(q) != 0 {
			return fmt.Errorf("%s: conflicts with deploy.resources; specify the same value in both places or omit one", field)
		}
	}
	*target = q.String()
	return nil
}
