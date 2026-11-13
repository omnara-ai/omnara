package jsoncanonical

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"strconv"
	"strings"
)

type ValueError struct {
	Pointer jsontext.Pointer
	Err     error
}

func (e *ValueError) Error() string { return string(e.Pointer) + ": " + e.Err.Error() }
func (e *ValueError) Unwrap() error { return e.Err }

func Canonical(raw json.RawMessage) (json.RawMessage, error) {
	checked := jsontext.Value(bytes.Clone(raw))
	if err := checked.Format(); err != nil {
		return nil, err
	}
	value, err := decode(checked)
	if err != nil {
		return nil, err
	}
	if value, err = canonicalValue(value, ""); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func canonicalValue(value any, path jsontext.Pointer) (any, error) {
	var err error
	switch typed := value.(type) {
	case json.Number:
		number, err := Number(string(typed))
		if err != nil {
			return nil, &ValueError{Pointer: path, Err: err}
		}
		return number, nil
	case []any:
		for index := range typed {
			if typed[index], err = canonicalValue(typed[index], path.AppendToken(strconv.Itoa(index))); err != nil {
				return nil, err
			}
		}
	case map[string]any:
		for key := range typed {
			if typed[key], err = canonicalValue(typed[key], path.AppendToken(key)); err != nil {
				return nil, err
			}
		}
	}
	return value, nil
}

func Number(number string) (json.Number, error) {
	exact, err := plainDecimal(number)
	if err != nil {
		return "", err
	}
	float, err := strconv.ParseFloat(exact, 64)
	if err != nil || (float == 0 && exact != "0") {
		return "", errors.New("number must have a finite, nonzero float64 approximation or be zero")
	}
	encoded, err := json.Marshal(float)
	if err != nil {
		return "", err
	}
	if short, err := plainDecimal(string(encoded)); err == nil && short == exact {
		return json.Number(encoded), nil
	}
	return json.Number(exact), nil
}

const maxNumberDigits = 1000

func plainDecimal(number string) (string, error) {
	if len(number) > 2*maxNumberDigits {
		return "", errors.New("number literal exceeds 2000 characters")
	}
	sign := ""
	if strings.HasPrefix(number, "-") {
		sign, number = "-", number[1:]
	}
	exponent := 0
	if mantissa, power, found := strings.Cut(strings.ToLower(number), "e"); found {
		parsed, err := strconv.Atoi(power)
		if err != nil || parsed > maxNumberDigits || parsed < -maxNumberDigits {
			return "", errors.New("number exponent must be between -1000 and 1000")
		}
		exponent, number = parsed, mantissa
	}
	whole, fraction, _ := strings.Cut(number, ".")
	for _, digit := range whole + fraction {
		if digit < '0' || digit > '9' {
			return "", errors.New("invalid decimal number")
		}
	}
	if whole == "" && fraction == "" {
		return "", errors.New("invalid decimal number")
	}
	digits := strings.TrimLeft(whole+fraction, "0")
	point := len(whole) + exponent - (len(whole) + len(fraction) - len(digits))
	digits = strings.TrimRight(digits, "0")
	switch {
	case digits == "":
		return "0", nil
	case len(digits) > maxNumberDigits || point > maxNumberDigits || len(digits)-point > maxNumberDigits:
		return "", errors.New("number has too many digits")
	case point <= 0:
		return sign + "0." + strings.Repeat("0", -point) + digits, nil
	case point >= len(digits):
		return sign + digits + strings.Repeat("0", point-len(digits)), nil
	}
	return sign + digits[:point] + "." + digits[point:], nil
}
