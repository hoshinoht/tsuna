/** Guard the common sentinel-wait pattern; this is not a shell parser. */
export function backgroundJobViolation(input: Record<string, unknown>): string | undefined {
	const command = typeof input.command === "string" ? input.command : "";
	const fileLoop = /\b(?:while|until)\s+(?:!\s*)?(?:\[\[?|test)\s+[^;\n]*?-[ef](?=\s)/.test(command);
	if (fileLoop && /\bsleep\s/.test(command)) {
		return "File-polling sleep loops can outlive the process that writes the completion file. Use the native job handle and wait/proc status, or short single probes with a finite overall deadline and verified producer identity. If the producer is gone and the exit record is missing, report unknown/interrupted; never invent an exit code.";
	}
	// Named services have their own lifecycle and readiness timeout in OMP.
	const service = typeof input.name === "string" && input.name.trim().length > 0;
	if (!service && input.timeout === 0) {
		return "Ordinary shell commands require a finite positive timeout, including background jobs. Choose a deadline based on the expected runtime (for a six-minute suite, use about 900 seconds), then inspect job status and logs on expiry.";
	}
	return undefined;
}
