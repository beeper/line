package connector

import (
	"go.mau.fi/util/l10n"
)

var L = l10n.NewNamespace("line")

var (
	msgFlowEmailName        = L.Define("login.flow.email.name", "Login")
	msgFlowEmailDescription = L.Define("login.flow.email.description", "Login with your LINE Email and Password")
	msgFlowQRName           = L.Define("login.flow.qr.name", "QR Code")
	msgFlowQRDescription    = L.Define("login.flow.qr.description", "Scan a QR code with the LINE mobile app")

	msgFieldEmailName    = L.Define("login.field.email.name", "Email")
	msgFieldPasswordName = L.Define("login.field.password.name", "Password")

	msgStepCredsInstructions                = L.Define("login.step.enter_creds.instructions", "Please enter your LINE email and password.")
	msgStepCredsFailedInstructions          = L.Define("login.step.enter_creds.failed.instructions", "Could not log in to LINE. Please check your email and password and try again.")
	msgStepCredsTooManyAttemptsInstructions = L.Define("login.step.enter_creds.too_many_attempts.instructions", "Too many login attempts. LINE has locked login for this account temporarily.")
	msgStepCredsInvalidInstructions         = L.Define("login.step.enter_creds.invalid.instructions", "LINE rejected the email or password. Make sure you used the email from LINE Settings -> Account -> Email Address, then try again.")
	msgStepCredsErrorInstructions           = L.Define(
		"login.step.enter_creds.error.instructions", "Could not log in to LINE: {error}",
		l10n.WithComment("error is a message explaining the failure: either text returned by LINE or one of the login.error.* messages"),
	)
	msgErrorNoStoredCredentials = L.Define("login.error.no_stored_credentials", "No stored LINE credentials are available. Please enter your LINE email and password to reconnect.")
	msgErrorCredentialsRequired = L.Define("login.error.credentials_required", "Email and password are required")
	msgErrorRejected            = L.Define("login.error.rejected", "LINE rejected the sign-in. Please check your email and password and try again.")

	msgStepCredsQRRevokedInstructions = L.Define("login.step.enter_creds.qr_revoked.instructions", "LINE signed this QR login out right away. Log in with your LINE email and password instead.")

	msgStepWaitVerificationInstructions    = L.Define("login.step.wait_verification.instructions", "Please open the LINE app on your mobile device to complete the login.")
	msgStepWaitVerificationPINInstructions = L.Define(
		"login.step.wait_verification_pin.instructions", "Open LINE and enter this PIN on your mobile device: {pin}",
		l10n.WithComment("pin is the numeric verification code to enter in the LINE mobile app"),
	)
	msgStepEnterPINInstructions = L.Define(
		"login.step.enter_pin.instructions",
		"Please open the LINE app on your mobile device and enter this PIN code: **{pin}**\n\nAfter entering the code, click Continue below.",
		l10n.WithComment("pin is the numeric verification code to enter in the LINE mobile app"),
	)

	msgStepQRInstructions       = L.Define("login.step.qr.instructions", "Scan this QR code with the LINE mobile app.")
	msgStepQRPINInstructions    = L.Define("login.step.qr_pin.instructions", "Enter the displayed PIN in the LINE mobile app.")
	msgStepCompleteInstructions = L.Define("login.step.complete.instructions", "Successfully logged in")
)
