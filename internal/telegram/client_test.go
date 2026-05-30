package telegram

import (
	"testing"
)

func TestDispatchTelegramAlert_Execution(t *testing.T) {
	fakeToken := "123456789:dummy_token_for_unit_test"
	fakeChatID := "-10000000000"
	testMessage := "Test message from CI/CD pipeline"

	DispatchTelegramAlert(fakeToken, fakeChatID, testMessage)
}
