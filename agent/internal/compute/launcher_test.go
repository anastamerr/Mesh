package compute

import (
	"reflect"
	"testing"
)

func TestWSLPathArgumentsUsePortableShortFlags(t *testing.T) {
	path := `F:\Mesh\storage\collection`
	got := wslPathArguments("Mesh", path)
	want := []string{"--distribution", "Mesh", "--user", "root", "--exec", "wslpath", "-a", "-u", path}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected WSL path arguments: %#v", got)
	}
}
