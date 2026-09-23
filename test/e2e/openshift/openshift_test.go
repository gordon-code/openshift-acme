package extended

import (
	"testing"

	_ "github.com/tnozicka/openshift-acme/test/e2e/openshift/routes"
	exutil "github.com/tnozicka/openshift-acme/test/e2e/openshift/util"
)

func TestExtended(t *testing.T) {
	exutil.InitTest(t)
	exutil.ExecuteTest(t, "Extended")
}
