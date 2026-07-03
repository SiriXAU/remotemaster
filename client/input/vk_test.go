package input

import "testing"

func TestIsExtendedVK(t *testing.T) {
	extended := []int{
		0x25, // VK_LEFT
		0x26, // VK_UP
		0x2E, // VK_DELETE
		0x24, // VK_HOME
		0x5B, // VK_LWIN
		0x6F, // VK_DIVIDE
		0xA3, // VK_RCONTROL
		0xA5, // VK_RMENU
	}
	for _, vk := range extended {
		if !IsExtendedVK(vk) {
			t.Errorf("IsExtendedVK(%#x) = false, want true", vk)
		}
	}

	notExtended := []int{
		0x41, // 'A'
		0x0D, // VK_RETURN (main Enter)
		0xA0, // VK_LSHIFT
		0xA2, // VK_LCONTROL
		0x60, // VK_NUMPAD0
		0x20, // VK_SPACE
	}
	for _, vk := range notExtended {
		if IsExtendedVK(vk) {
			t.Errorf("IsExtendedVK(%#x) = true, want false", vk)
		}
	}
}
