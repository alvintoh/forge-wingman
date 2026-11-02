/** projectName is a repository's name without its owner: "alvintoh/forge-wingman" reads "forge-wingman". */
export function projectName(repository: string): string {
  return repository.slice(repository.indexOf("/") + 1);
}
