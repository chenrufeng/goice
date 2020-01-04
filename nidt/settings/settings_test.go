package settings

import(
	"testing"
)

func Test_Init_EmptyString_ReturnError(t *testing.T){	
	if Init("") == nil {
		t.Error("")
	}
	unInit()
}