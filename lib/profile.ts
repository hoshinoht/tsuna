import { join } from "node:path";

/** OMP's command-backed key setting supports direct official launches without exporting a secret. */
export function gatewayKeySetting(root: string): string {
	const keyFile = join(root, ".runtime/proxy/client-key");
	return `!cat '${keyFile.replaceAll("'", "'\\''")}'`;
}
