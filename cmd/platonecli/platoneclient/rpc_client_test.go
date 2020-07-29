package platoneclient

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/PlatONEnetwork/PlatONE-Go/rlp"
)

const (
	arg0        = "this is a test"
	arg1 uint64 = 17
	arg2 bool   = true
)

var types = []string{"string", "uint64", "bool"}
var expResult = fmt.Sprintf("%s %d %v ", arg0, arg1, arg2)

func rlpEncode(params ...interface{}) []byte {

	bin, err := rlp.EncodeToBytes(params)
	if nil != err {
		panic("error")
	}
	return bin
}

func rlpDecode(bin []byte) interface{} {
	var result interface{}
	_ = rlp.DecodeBytes(bin, &result)
	return result
}

func TestParseReceiptLogData(t *testing.T) {

	bin := rlpEncode(arg0, arg1, arg2)
	result := rlpDecode(bin)
	strResult := parseReceiptLogData(result.([]interface{}), types)
	assert.Equal(t, expResult, strResult, "FAILED")
	t.Logf("the result is %v type: %v\n", result, reflect.TypeOf(result))
	t.Logf("the strResult is %v\n", strResult)
}