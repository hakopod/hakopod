"""Signed, expiring installation entitlements and explicit Free/Pro catalog."""
schemas["LicenseFeature"] = obj({"id":S,"name":S,"plan":{"type":"string","enum":["free","pro"]},"description":S,"enabled":B},["id","name","plan","description","enabled"])
schemas["LicenseStatus"] = obj({"installation_id":S,"revision":I,"plan":{"type":"string","enum":["free","pro"]},"state":S,"valid":B,"issuer_configured":B,"licensed_to":S,"license_id":S,"expires_at":{"anyOf":[T,{"type":"null"}]},"sequence":I,"features":array(S),"catalog":array(ref("LicenseFeature"))},["installation_id","revision","plan","state","valid","issuer_configured","licensed_to","license_id","expires_at","sequence","features","catalog"])
route("/license","get","getLicenseStatus",ref("LicenseStatus"))
route("/license","put","activateLicense",ref("LicenseStatus"),obj({"license":S,"expected_revision":I},["license","expected_revision"]))
route("/license","delete","removeLicense",ref("LicenseStatus"),obj({"expected_revision":I},["expected_revision"]))
