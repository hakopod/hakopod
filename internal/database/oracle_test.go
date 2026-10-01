package database

import (
	"strings"
	"testing"
)

func TestOracleFreeQuotaReservesSystemData(t *testing.T) {
	for storage, quota := range map[int64]int64{0: 0, 8: 0, 10: 2, 15: 7, 18: 10, 1024: 10} {
		if got := OracleFreeQuotaGiB(storage); got != quota {
			t.Fatalf("%d GiB volume: got %d GiB user quota, want %d", storage, got, quota)
		}
	}
}

func TestOracleEditionAndImageEntitlements(t *testing.T) {
	free := OracleConfig{Edition: "free"}
	if err := free.Validate("23.26", "standalone"); err != nil || free.ContainerImage() != OracleFreeImage {
		t.Fatal("Oracle Free contract rejected", err)
	}
	if err := free.Validate("23.26", "cluster"); err == nil {
		t.Fatal("Free edition admitted Data Guard")
	}
	image := "registry.example.com/customer/oracle:19.30@sha256:" + strings.Repeat("a", 64)
	enterprise := OracleConfig{Edition: "enterprise", Image: image, RegistryCredential: "licensed-oracle", LicenseConfirmed: true}
	for _, mode := range []string{"standalone", "cluster"} {
		if err := enterprise.Validate("19", mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, mutate := range []func(*OracleConfig){
		func(o *OracleConfig) { o.LicenseConfirmed = false },
		func(o *OracleConfig) { o.Image = "registry.example.com/customer/oracle:latest" },
		func(o *OracleConfig) { o.RegistryCredential = "other-project/registry" },
		func(o *OracleConfig) { o.Edition = "open-source" },
	} {
		next := enterprise
		mutate(&next)
		if next.Validate("19", "cluster") == nil {
			t.Fatal("invalid Oracle entitlement admitted")
		}
	}
	for _, next := range []OracleConfig{{Edition: "free", Image: image}, {Edition: "free", RegistryCredential: "private"}, {Edition: "free", LicenseConfirmed: true}} {
		if next.Validate("23.26", "standalone") == nil {
			t.Fatal("Enterprise configuration was silently ignored on Free")
		}
	}
}

func TestOracleImagesRejectCredentialsAndMutableReferences(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	for _, image := range []string{
		"registry.example.com/oracle" + digest,
		"registry.example.com:5000/customer/oracle-db:19.30" + digest,
	} {
		if err := validateOracleImage(image); err != nil {
			t.Fatal("valid image rejected", err)
		}
	}
	for _, image := range []string{
		"https://registry.example.com/oracle" + digest,
		"user:password@registry.example.com/oracle" + digest,
		"registry.example.com:password/oracle" + digest,
		"registry.example.com/oracle:latest",
		"oracle" + digest,
		"team/oracle" + digest,
		"registry.example.com/../oracle" + digest,
		"registry.example.com//oracle" + digest,
		"registry.example.com/oracle?token=private" + digest,
		"registry.example.com/oracle" + strings.ToUpper(digest),
	} {
		if validateOracleImage(image) == nil {
			t.Fatal("unsafe image accepted")
		}
	}
}
