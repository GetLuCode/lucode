package plugins

import (
	"errors"
	"math/big"
	"reflect"

	"github.com/dop251/goja"
)

// BigInt arithmetic is one instruction that cannot be interrupted: 2n**(2n**40n) runs and allocates
// until the machine gives out. The global is gone, so a literal in the source is the only way to
// get one, and a file with one is refused when it compiles.
func checkLiterals(file, src string) error {
	prg, err := goja.Parse(file, src)
	if err != nil {
		return nil // the compiler says what is wrong with it
	}
	if hasBigInt(reflect.ValueOf(prg), map[uintptr]bool{}) {
		return errors.New("BigInt values are not available in the plugin runtime")
	}
	return nil
}

var bigIntType = reflect.TypeOf((*big.Int)(nil))

func hasBigInt(v reflect.Value, seen map[uintptr]bool) bool {
	if v.IsValid() && v.Kind() == reflect.Interface && !v.IsNil() {
		if v.Elem().Type() == bigIntType {
			return true
		}
	}
	switch v.Kind() {
	case reflect.Interface:
		return !v.IsNil() && hasBigInt(v.Elem(), seen)
	case reflect.Pointer:
		if v.IsNil() || seen[v.Pointer()] {
			return false
		}
		seen[v.Pointer()] = true
		return hasBigInt(v.Elem(), seen)
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if hasBigInt(v.Index(i), seen) {
				return true
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if hasBigInt(v.Field(i), seen) {
				return true
			}
		}
	}
	return false
}
