package api

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/framework"
)

func buildSecretEnvironment(secrets map[string]string) string {
	if len(secrets) == 0 {
		return ""
	}
	var s strings.Builder
	s.WriteString("        env:\n")
	for i, id := range framework.SecretIDs(secrets) {
		s.WriteString(fmt.Sprintf("          HAKOPOD_BUILD_SECRET_%d: ${{ secrets['%s'] }}\n", i, secrets[id]))
	}
	return s.String()
}
func buildSecretInputs(secrets map[string]string) string {
	if len(secrets) == 0 {
		return ""
	}
	var s strings.Builder
	s.WriteString("          secret-envs: |\n")
	for i, id := range framework.SecretIDs(secrets) {
		s.WriteString(fmt.Sprintf("            %s=HAKOPOD_BUILD_SECRET_%d\n", id, i))
	}
	return s.String()
}
func buildSecretCheck(secrets map[string]string) string {
	if len(secrets) == 0 {
		return ""
	}
	var s strings.Builder
	s.WriteString("      - name: Check required build secrets\n" + buildSecretEnvironment(secrets) + "        run: |\n          set +x\n")
	for i := range framework.SecretIDs(secrets) {
		s.WriteString(fmt.Sprintf("          test -n \"$HAKOPOD_BUILD_SECRET_%d\" || { echo 'A required repository build secret is missing'; exit 1; }\n", i))
	}
	return s.String()
}
func gitlabSecretFlags(secrets map[string]string) string {
	var s strings.Builder
	for _, id := range framework.SecretIDs(secrets) {
		s.WriteString(" --secret " + strconv.Quote("type=env,id="+id+",env="+secrets[id]))
	}
	return s.String()
}
func frameworkRecipe(c buildConfig) string {
	if c.Framework == nil {
		return ""
	}
	recipe, err := framework.Dockerfile(*c.Framework, c.BuildSecrets)
	if err != nil {
		return ""
	}
	var args strings.Builder
	for _, arg := range sortedBuildArguments(c.BuildArgs) {
		args.WriteString("ARG " + strings.SplitN(arg, "=", 2)[0] + "\n")
	}
	return strings.Replace(recipe, "COPY . .\n", "COPY . .\n"+args.String(), 1)
}
func frameworkSetup(c buildConfig, destination string) string {
	content := base64.StdEncoding.EncodeToString([]byte(frameworkRecipe(c)))
	// Append exclusions to the existing ignore file. Never overwrite the
	// repository's own exclusions, or copy Git metadata into the runtime image.
	ignore := strconv.Quote(c.ContextPath + "/.dockerignore")
	guard := "hakopod_checkout=$(pwd -P)\nhakopod_context=$(cd " + strconv.Quote(c.ContextPath) + " && pwd -P)\n" +
		"case \"$hakopod_context\" in \"$hakopod_checkout\"|\"$hakopod_checkout\"/*) ;; *) echo 'Build context must remain inside the repository'; exit 1;; esac\n" +
		"test ! -L " + ignore + " && { test ! -e " + ignore + " || test -f " + ignore + "; } || { echo 'Build ignore file must be a regular file'; exit 1; }\n"
	return guard + "printf '%s' '" + content + "' | base64 -d > " + destination + "\n" +
		"printf '\\n.git\\n.env\\n.env.*\\nnode_modules\\n' >> " + strconv.Quote(c.ContextPath+"/.dockerignore") + "\n"
}
