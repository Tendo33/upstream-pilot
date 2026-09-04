import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";

const root = process.cwd();
const chunks = ["Upstream Pilot — third-party license texts", "Generated from the installed, locked dependencies. Project attribution: THIRD_PARTY_NOTICES.md."];
const missing = [];
function append(name, version, directory) {
  const names = fs.readdirSync(directory).filter(n => /^(licen[sc]e|copying|notice)([.-].*)?$/i.test(n));
  const texts = names.filter(n => fs.statSync(path.join(directory,n)).isFile()).map(n => fs.readFileSync(path.join(directory,n),"utf8"));
  if (!texts.length) {
    const fallback = name.startsWith("@radix-ui/") ? "radix-primitives" : name === "react-remove-scroll-bar" ? "react-remove-scroll-bar" : null;
    if (fallback) texts.push(fs.readFileSync(path.join(root,"licenses",fallback+".txt"),"utf8"));
    else { missing.push(name); return; }
  }
  chunks.push(`\n${"=".repeat(72)}\n${name} ${version}\n${"=".repeat(72)}\n${texts.join("\n\n")}`);
}
const modules = JSON.parse(execFileSync("go", ["list", "-m", "-json", "all"], {encoding:"utf8"}).replace(/}\s*{/g, "},{").replace(/^/,"[").replace(/\s*$/,"]"));
for (const m of modules) if (!m.Main && m.Dir) append(m.Path,m.Version,m.Dir);
const lock = JSON.parse(fs.readFileSync("web/package-lock.json","utf8"));
for (const [location,m] of Object.entries(lock.packages)) {
  if (!location || m.dev) continue;
  const directory = path.join(root,"web",location);
  const pkg = JSON.parse(fs.readFileSync(path.join(directory,"package.json"),"utf8"));
  append(pkg.name,m.version,directory);
}
if (missing.length) throw new Error(`License text missing: ${missing.join(", ")}`);
fs.writeFileSync("THIRD_PARTY_LICENSES.txt", chunks.join("\n\n").replace(/\r\n/g,"\n").trimEnd()+"\n");
console.log("Collected locked dependency license texts");
