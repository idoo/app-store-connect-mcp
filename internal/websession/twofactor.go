// Apple ID two-factor step of Login. Protocol details follow
// rudrankriyam/App-Store-Connect-CLI (MIT), internal/appleauth/twofactor.go
// and internal/web/auth.go.

package websession

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// twoFactorMaxSMSSwitches bounds how often the human may ask for another SMS.
const twoFactorMaxSMSSwitches = 3

// twoFactorMaxAttempts is how many codes the human may enter before Login
// gives up with LoginCodeRejected.
const twoFactorMaxAttempts = 3

// authOptions is GET /appleauth/auth's answer.
type authOptions struct {
	NoTrustedDevices    bool `json:"noTrustedDevices"`
	TrustedPhoneNumbers []struct {
		ID                 int    `json:"id"`
		PushMode           string `json:"pushMode"`
		NumberWithDialCode string `json:"numberWithDialCode"`
	} `json:"trustedPhoneNumbers"`
	SecurityCode struct {
		Length int `json:"length"`
	} `json:"securityCode"`
}

// twoFactor completes Apple's 409 challenge: prompt for a trusted-device
// code (or SMS on request), verify it, then trust the browser session.
func (f *loginFlow) twoFactor(ctx context.Context, prompt func(context.Context, TwoFactor) (string, error)) error {
	if prompt == nil {
		return &LoginError{Kind: LoginTwoFactorUnavailable, Stage: "two_factor"}
	}
	resp, body, err := f.idmsaJSON(ctx, http.MethodGet, "/appleauth/auth", nil, "")
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newLoginError("auth_options", resp, body)
	}
	var opts authOptions
	if json.Unmarshal(body, &opts) != nil {
		return &LoginError{Kind: LoginUnexpected, Stage: "auth_options", Status: resp.StatusCode}
	}
	challenge := TwoFactor{Kind: "trusted_device", CodeLength: opts.SecurityCode.Length}
	if challenge.CodeLength <= 0 {
		challenge.CodeLength = 6
	}
	for _, p := range opts.TrustedPhoneNumbers {
		challenge.Phones = append(challenge.Phones, sanitizePhone(p.NumberWithDialCode))
	}

	phone := -1 // index into TrustedPhoneNumbers for the SMS path
	if opts.NoTrustedDevices {
		if len(opts.TrustedPhoneNumbers) == 0 {
			return &LoginError{Kind: LoginTwoFactorUnavailable, Stage: "auth_options"}
		}
		phone = 0
		if err := f.requestSMS(ctx, opts.TrustedPhoneNumbers[0].ID); err != nil {
			return err
		}
		challenge.Kind = "sms"
	}

	switches := 0
	for attempt := 1; ; attempt++ {
		code, err := f.promptCode(ctx, prompt, &challenge, &opts, &phone, &switches)
		if err != nil {
			return err
		}
		err = f.verifyCode(ctx, code, &opts, phone)
		if err == nil {
			break
		}
		var le *LoginError
		if !errors.As(err, &le) || le.Kind != LoginCodeRejected || attempt >= twoFactorMaxAttempts {
			return err
		}
		challenge.Rejected = true
	}

	resp, body, err = f.idmsaJSON(ctx, http.MethodGet, "/appleauth/auth/2sv/trust", nil, "")
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newLoginError("trust", resp, body)
	}
	return nil
}

// requestSMS asks Apple to text a code to trusted phone id.
func (f *loginFlow) requestSMS(ctx context.Context, id int) error {
	data, err := json.Marshal(map[string]any{"phoneNumber": map[string]int{"id": id}, "mode": "sms"})
	if err != nil {
		return errLoginf("encode SMS request")
	}
	resp, body, err := f.idmsaJSON(ctx, http.MethodPut, "/appleauth/auth/verify/phone", data, "")
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newLoginError("request_sms", resp, body)
	}
	return nil
}

// parseSMSReply recognises "sms:<n>"; n is 0 when it is not a number.
func parseSMSReply(reply string) (int, bool) {
	rest, ok := strings.CutPrefix(strings.ToLower(reply), "sms:")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil {
		return 0, true
	}
	return n, true
}

// sanitizePhone keeps Apple's masked number printable and short.
func sanitizePhone(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 48 {
			break
		}
		if r >= 0x20 && r != 0x7f && (r < 0x80 || r == '•' || r >= 0xa0) && r != 0x2028 && r != 0x2029 {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// promptCode asks the human for a code; "sms:<n>" replies switch delivery to
// phone n (updating challenge and phone) and ask again.
func (f *loginFlow) promptCode(ctx context.Context, prompt func(context.Context, TwoFactor) (string, error),
	challenge *TwoFactor, opts *authOptions, phone, switches *int,
) (string, error) {
	for {
		reply, err := prompt(ctx, *challenge)
		if err != nil {
			return "", errLoginf("two-factor prompt: %w", err)
		}
		reply = strings.TrimSpace(reply)
		n, isSMS := parseSMSReply(reply)
		if !isSMS {
			if reply == "" {
				return "", errLoginf("no two-factor code entered")
			}
			return reply, nil
		}
		if *switches >= twoFactorMaxSMSSwitches {
			return "", errLoginf("too many SMS requests; run the login again")
		}
		*switches++
		if n < 1 || n > len(opts.TrustedPhoneNumbers) {
			return "", errLoginf("there is no trusted phone number %d (Apple lists %d)", n, len(opts.TrustedPhoneNumbers))
		}
		*phone = n - 1
		if err := f.requestSMS(ctx, opts.TrustedPhoneNumbers[*phone].ID); err != nil {
			return "", err
		}
		challenge.Kind = "sms"
		challenge.Rejected = false
	}
}

// verifyCode submits code to the trusted-device endpoint (phone < 0) or the
// phone endpoint for opts.TrustedPhoneNumbers[phone].
func (f *loginFlow) verifyCode(ctx context.Context, code string, opts *authOptions, phone int) error {
	var stage, path string
	var payload any
	if phone < 0 {
		stage, path = "verify_trusted_device", "/appleauth/auth/verify/trusteddevice/securitycode"
		payload = map[string]any{"securityCode": map[string]string{"code": code}}
	} else {
		stage, path = "verify_phone", "/appleauth/auth/verify/phone/securitycode"
		payload = map[string]any{
			"securityCode": map[string]string{"code": code},
			"phoneNumber":  map[string]int{"id": opts.TrustedPhoneNumbers[phone].ID},
			"mode":         "sms",
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return errLoginf("encode two-factor code")
	}
	resp, body, err := f.idmsaJSON(ctx, http.MethodPost, path, data, "")
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newLoginError(stage, resp, body)
	}
	return nil
}
