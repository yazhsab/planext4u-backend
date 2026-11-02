# Android device permission controls

The shared Privacy settings screen opens device permissions in all three normal apps. The native bridge reads Android access to location, camera, microphone and notifications; it never reads the sensors or account data. Location distinguishes approximate from precise and separately reports whether device location services are enabled. The plugin declares no new permission itself. Missing manifest permissions and unsupported notification state are unavailable.

A permission request requires the feature explanation and an explicit Continue to Android action. Cancel does not call the OS. Settings destinations are a fixed allowlist bound to the current package; arbitrary URIs, permissions and package names are rejected. Revocation and settings return refresh current OS state. An unavailable refresh removes old displayed grants; disposal discards late results. App activity loss interrupts pending requests without granting access. Permission state never grants server consent, a business capability, a push subscription or document approval.

Android provides no definitive permanent-denial bit. The screen offers app settings explicitly and does not fabricate one from a remembered request. Pre-Android-13 notifications use actual app notification status; API 23 reports this status unavailable. Channel-specific notification preferences remain separate. Contacts/matching, biometric unlock, hardware capture and iOS are separate work.

The native test uses only the dedicated synthetic AVD, actual camera deny/allow dialogs and the actual location settings activity. The companion driver is bounded to that AVD, expected checkpoints and OS-owned controls. It restores the test location service setting and retains failures. No physical device, provider or production-release acceptance is implied.

Implementation follows [Android runtime permissions](https://developer.android.com/training/permissions/requesting) and [notification permission](https://developer.android.com/develop/ui/compose/notifications/notification-permission). The Phase 2 register records verified results separately from implementation.
