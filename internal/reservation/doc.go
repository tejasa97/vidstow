// Package reservation chooses complete, collision-free output artifact sets.
//
// It deliberately has no dependency on the queue manager, durable store, or
// download engine. The caller supplies engine-rendered declarations, a
// platform-aware comparison policy, and an identity-validating availability
// probe. A reservation is only a claim made by the durable transaction that
// invokes SelectionCallback; this package never creates destination files or
// locks State. The publisher must still use a no-replace primitive.
package reservation
