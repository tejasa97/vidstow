// Package reservationfs adapts a caller-owned output directory to the
// reservation package's platform-neutral contracts.
//
// OpenRoot keeps an OS directory handle for the lifetime of a Root. The
// returned Facts value contains the reservation.Volume, native comparison
// policies, and an identity-validating AvailabilityProbe. The caller owns the
// Root and must close it when those facts are no longer needed.
//
// This package only observes the filesystem. It does not create reservations,
// publish files, lock application State, or schedule jobs.
package reservationfs
