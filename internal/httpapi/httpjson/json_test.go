package httpjson

import "testing"

func TestValidateUnicode(t *testing.T) {
	invalidUTF8 := append([]byte(`{"name":"Acme`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`Labs"}`)...)
	for name, test := range map[string]struct {
		body    []byte
		wantErr bool
	}{
		"invalid UTF-8":        {body: invalidUTF8, wantErr: true},
		"unpaired high escape": {body: []byte(`{"name":"Acme\ud800Labs"}`), wantErr: true},
		"unpaired low escape":  {body: []byte(`{"name":"Acme\udc00Labs"}`), wantErr: true},
		"mismatched pair":      {body: []byte(`{"name":"Acme\ud800\u0041Labs"}`), wantErr: true},
		"surrogate pair":       {body: []byte(`{"name":"Acme\ud83d\ude80Labs"}`)},
		"replacement rune":     {body: []byte(`{"name":"Acme�Labs"}`)},
		"literal escape text":  {body: []byte(`{"name":"Acme\\ud800Labs"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateUnicode(test.body)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateUnicode() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}
