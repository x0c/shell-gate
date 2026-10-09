// 回显计时口径的守护测试：验证「终端能力查询帧」不会被当成按键回显。
//
// 为什么单独用 node 测：这段逻辑是正则密集的字节处理，Go 侧只能断言源码里有没有某个
// 字符串，测不出正则写错。2026-08-05 第一版就把 tmux passthrough 的范围写错了
// （用「不含 ESC」界定，而内部转义序列的 ESC 是成对写的），只有真跑一遍才发现。
//
// 由 server 包的 TestEnhanceQueryFrameFilter 调起；本机没装 node 时该用例自动跳过。
import fs from "fs";
import path from "path";
import { fileURLToPath } from "url";

const here = path.dirname(fileURLToPath(import.meta.url));
const src = fs.readFileSync(path.join(here, "enhance.js"), "utf8");

// 从 enhance.js 里原样抠出被测函数，避免测试和实现各写一份逻辑。
const pick = (name) => {
  const i = src.indexOf("function " + name);
  if (i < 0) throw new Error("enhance.js 里找不到函数 " + name);
  let depth = 0;
  for (let k = src.indexOf("{", i); k < src.length; k++) {
    if (src[k] === "{") depth++;
    else if (src[k] === "}" && --depth === 0) return src.slice(i, k + 1);
  }
  throw new Error("函数 " + name + " 括号不闭合");
};
const fn = new Function(
  pick("ttydOutputPayload") + "\n" + pick("isTerminalQueryOnlyFrame") +
  "\nreturn { ttydOutputPayload, isTerminalQueryOnlyFrame };"
)();

// 真机抓到的 Corral 空闲查询帧（48 字节，每 2.00 秒一发），前面补 ttyd 的 '0' 命令码。
const CORRAL_IDLE =
  "0\x1b[?996n\x1b]11;?\x07\x1bPtmux;\x1b\x1b[?996n\x1b\\\x1bPtmux;\x1b\x1b]11;?\x07\x1b\\";
const bin = (s) => new Uint8Array([...s].map((c) => c.charCodeAt(0)));

const cases = [
  ["Corral 空闲查询帧（字符串形态）", CORRAL_IDLE, true],
  ["Corral 空闲查询帧（二进制形态）", bin(CORRAL_IDLE), true],
  ["单独的 OSC 11 背景色查询", "0\x1b]11;?\x07", true],
  ["单独的 CSI DSR 配色方案查询", "0\x1b[?996n", true],
  ["普通字符回显", "0a", false],
  ["带光标定位的重绘", "0\x1b[5;1Hhello", false],
  ["清屏重绘", "0\x1b[2J\x1b[H$ ", false],
  ["OSC 11 的应答（带颜色值，属于内容）", "0\x1b]11;rgb:0000/0000/0000\x07", false],
  ["空载荷", "0", false],
];

let failed = 0;
for (const [name, data, want] of cases) {
  const got = fn.isTerminalQueryOnlyFrame(fn.ttydOutputPayload(data));
  if (got !== want) {
    failed++;
    console.error(`失败  ${name}：判定为查询帧=${got}，期望=${want}`);
  }
}
if (failed) {
  console.error(`${failed} 个用例未通过`);
  process.exit(1);
}
console.log(`回显计时口径守护测试全部通过（${cases.length} 个用例）`);
