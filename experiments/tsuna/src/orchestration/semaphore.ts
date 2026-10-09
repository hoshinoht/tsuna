/**
 * Counting semaphore for spawn permits.
 *
 * Copied from Oh My Pi `packages/coding-agent/src/task/parallel.ts`
 * (`normalizeConcurrencyLimit`, `Semaphore`, `semaphoreAbortReason`) at v18.8.6
 * (f068751e2f1dbdbc195977776d47a26db8697495). MIT License:
 * Copyright (c) 2025 Mario Zechner; Copyright (c) 2025-2026 Can Bölük;
 * Copyright (c) 2026 Stencil Labs, Inc. See third_party/licenses/oh-my-pi.LICENSE.
 * Unmodified apart from this header and the added `inFlight`/`waiting` getters.
 */
/**
 * Simple counting semaphore for limiting concurrency across independently-scheduled async work.
 *
 * `max <= 0` (or any non-finite input) means unbounded — every `acquire()` resolves
 * immediately — matching `task.maxConcurrency = 0`'s "Unlimited" semantics in the
 * settings UI ([#3305](https://github.com/can1357/oh-my-pi/issues/3305)).
 */
export function normalizeConcurrencyLimit(max: number): number {
	const normalizedMax = Number.isFinite(max) ? Math.trunc(max) : 0;
	return normalizedMax > 0 ? normalizedMax : 0;
}

export class Semaphore {
	#max: number;
	#current = 0;
	#queue: Array<() => void> = [];

	constructor(max: number) {
		const normalizedMax = normalizeConcurrencyLimit(max);
		this.#max = normalizedMax > 0 ? normalizedMax : Number.POSITIVE_INFINITY;
	}

	/**
	 * Resolves when a slot is available. Pass an `AbortSignal` so callers that
	 * stop waiting (parent task cancelled, wall-clock budget elapsed) also stop
	 * occupying a queue slot — otherwise a later `release()` would resolve the
	 * abandoned waiter, permanently shrinking effective concurrency for the
	 * remaining lifetime of the process (issue #3464 review feedback).
	 */
	async acquire(signal?: AbortSignal): Promise<void> {
		if (signal?.aborted) {
			throw semaphoreAbortReason(signal);
		}
		if (this.#current < this.#max) {
			this.#current++;
			return;
		}
		const { promise, resolve, reject } = Promise.withResolvers<void>();
		const queue = this.#queue;
		let waiter: () => void = resolve;
		if (signal) {
			const onAbort = () => {
				const index = queue.indexOf(waiter);
				if (index >= 0) queue.splice(index, 1);
				reject(semaphoreAbortReason(signal));
			};
			waiter = () => {
				signal.removeEventListener("abort", onAbort);
				resolve();
			};
			signal.addEventListener("abort", onAbort, { once: true });
		}
		queue.push(waiter);
		return promise;
	}

	/** Permits currently held (diagnostics/tests). */
	get inFlight(): number {
		return this.#current;
	}

	/** Queued acquirers (diagnostics/tests). */
	get waiting(): number {
		return this.#queue.length;
	}

	release(): void {
		if (this.#current > 0) this.#current--;
		// Admit the next waiter only if we are under the (possibly just-lowered) ceiling.
		if (this.#current < this.#max) {
			const next = this.#queue.shift();
			if (next) {
				this.#current++;
				next();
			}
		}
	}

	/**
	 * Adjust the maximum concurrency in place. Raising the ceiling immediately
	 * admits queued waiters that now fit; lowering it lets in-flight holders
	 * drain naturally (new acquires keep blocking until `#current` falls below
	 * the new max). Resizing the single shared instance — instead of replacing
	 * it — keeps in-flight slots counted, so a runtime or mixed limit change can
	 * never push concurrency past the cap (issue #3464 review feedback).
	 */
	resize(max: number): void {
		const normalizedMax = normalizeConcurrencyLimit(max);
		this.#max = normalizedMax > 0 ? normalizedMax : Number.POSITIVE_INFINITY;
		while (this.#current < this.#max) {
			const next = this.#queue.shift();
			if (!next) break;
			this.#current++;
			next();
		}
	}
}

function semaphoreAbortReason(signal: AbortSignal): unknown {
	const reason = signal.reason;
	if (reason !== undefined) return reason;
	return new Error("Semaphore acquire aborted");
}
