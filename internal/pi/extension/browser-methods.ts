import { Type } from "@sinclair/typebox";

export const BROWSER_TOOL_CATEGORIES = [
  "navigation",
  "interaction",
  "artifact_generation",
  "inspection",
  "external_effect",
  "network_mutation",
  "credential_auth",
  "composite",
] as const;

export type BrowserToolCategory = (typeof BROWSER_TOOL_CATEGORIES)[number];

export const BROWSER_METHOD_CATEGORY = {
  navigate: "navigation",
  back: "navigation",
  forward: "navigation",
  reload: "navigation",
  list_pages: "navigation",
  switch_page: "navigation",
  close_page: "navigation",
  list_frames: "navigation",
  select_frame: "navigation",
  click: "interaction",
  type: "interaction",
  press: "interaction",
  hover: "interaction",
  scroll: "interaction",
  select_option: "interaction",
  set_checked: "interaction",
  drag: "interaction",
  set_viewport: "interaction",
  click_ref: "interaction",
  hover_ref: "interaction",
  fill_ref: "interaction",
  emulate_device: "interaction",
  upload_file: "artifact_generation",
  debug_bundle: "artifact_generation",
  screenshot: "artifact_generation",
  zoom_region: "artifact_generation",
  save_pdf: "artifact_generation",
  visual_diff: "artifact_generation",
  generate_test: "artifact_generation",
  har_export: "artifact_generation",
  trace_start: "artifact_generation",
  trace_stop: "artifact_generation",
  snapshot: "inspection",
  get_ref: "inspection",
  accessibility_tree: "inspection",
  find: "inspection",
  page_source: "inspection",
  assert: "inspection",
  diff: "inspection",
  wait_for: "inspection",
  analyze_form: "inspection",
  find_best: "inspection",
  console: "inspection",
  network: "inspection",
  dialog: "inspection",
  timeline: "inspection",
  session_summary: "inspection",
  extract: "inspection",
  action_cache: "inspection",
  check_injection: "inspection",
  eval: "external_effect",
  fill_form: "external_effect",
  act: "external_effect",
  mock_route: "network_mutation",
  block_urls: "network_mutation",
  clear_routes: "network_mutation",
  save_state: "credential_auth",
  restore_state: "credential_auth",
  vault_save: "credential_auth",
  vault_login: "credential_auth",
  vault_list: "credential_auth",
  batch: "composite",
} as const satisfies Record<string, BrowserToolCategory>;

export const BROWSER_TOOL_METHODS = Object.keys(BROWSER_METHOD_CATEGORY) as BrowserToolMethod[];

export type BrowserToolMethod = keyof typeof BROWSER_METHOD_CATEGORY;

export const BrowserToolMethodSchema = Type.Union(
  BROWSER_TOOL_METHODS.map((method) => Type.Literal(method)) as any,
);

export const BrowserToolCategorySchema = Type.Union(
  BROWSER_TOOL_CATEGORIES.map((category) => Type.Literal(category)) as any,
);
