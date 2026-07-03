package input

// extendedVKs is the set of Windows virtual-key codes that occupy the
// "extended" range of the original PC keyboard layout. SendInput needs
// KEYEVENTF_EXTENDEDKEY for these or some applications (and the low-level
// hook chain) see the numpad twin instead — e.g. VK_LEFT without the flag is
// indistinguishable from numpad 4.
var extendedVKs = map[int]bool{
	0x21: true, // VK_PRIOR (Page Up)
	0x22: true, // VK_NEXT (Page Down)
	0x23: true, // VK_END
	0x24: true, // VK_HOME
	0x25: true, // VK_LEFT
	0x26: true, // VK_UP
	0x27: true, // VK_RIGHT
	0x28: true, // VK_DOWN
	0x2C: true, // VK_SNAPSHOT (Print Screen)
	0x2D: true, // VK_INSERT
	0x2E: true, // VK_DELETE
	0x5B: true, // VK_LWIN
	0x5C: true, // VK_RWIN
	0x5D: true, // VK_APPS (context menu)
	0x6F: true, // VK_DIVIDE (numpad /)
	0x90: true, // VK_NUMLOCK
	0xA3: true, // VK_RCONTROL
	0xA5: true, // VK_RMENU (right Alt / AltGr)
}

// IsExtendedVK reports whether vk must be injected with the extended-key
// flag set.
func IsExtendedVK(vk int) bool {
	return extendedVKs[vk]
}
