# Statsig to LaunchDarkly Migration Test Files

This directory contains comprehensive test files demonstrating all Statsig SDK features that need to be migrated to LaunchDarkly. These files serve as test cases for the migration skill.

## Test Files

### 1. `vanilla-js-app.js`
**Plain JavaScript implementation** featuring:
- StatsigClient initialization with complex user context
- Feature gates with various naming conventions (hyphens, underscores)
- Dynamic configs for homepage, pricing, themes, and limits
- Multiple experiments (checkout flow, onboarding, search, pricing)
- Session replay and autocapture setup
- Custom event logging
- Manual exposure logging
- Override capabilities for testing

### 2. `react-app.jsx`
**React application** demonstrating:
- StatsigProvider with plugins configuration
- All React hooks: `useGateValue`, `useConfig`, `useExperiment`, `useLayer`, `useStatsigClient`
- Complex component hierarchy with feature gates
- Dynamic config usage in components
- Multiple experiment implementations
- Session replay and autocapture in React context
- User context updates
- Both async and synchronous provider patterns

### 3. `typescript-app.ts`
**Fully-typed TypeScript implementation** with:
- Complete type definitions and interfaces
- Type-safe feature gate checking
- Generic config types with proper typing
- Typed experiment parameters
- Error handling with TypeScript
- Class-based architecture
- Event logging with metadata types
- Async/await patterns with proper types

## Running the Migration Tests

### Prerequisites
1. Ensure you have the migration skill installed:
   ```bash
   ls ~/.claude/skills/statsig-to-launchdarkly-migrator/SKILL.md
   # or, if you installed the legacy compat shim instead:
   ls ~/.claude/agents/statsig-to-launchdarkly-sdk-migrator.md
   ```

2. Clone this repository if you haven't already:
   ```bash
   git clone https://github.com/yeutterg/claude-statsig-to-launchdarkly-sdk-migrator.git
   ```

### Testing the Migration Skill

1. **Test with JavaScript file:**
   ```
   In Claude Code, open vanilla-js-app.js and ask:
   "Please migrate this Statsig implementation to LaunchDarkly"
   ```

2. **Test with React file:**
   ```
   In Claude Code, open react-app.jsx and ask:
   "Convert this React app from Statsig to LaunchDarkly SDK"
   ```

3. **Test with TypeScript file:**
   ```
   In Claude Code, open typescript-app.ts and ask:
   "Migrate this TypeScript Statsig code to LaunchDarkly with proper types"
   ```

### Expected Migration Outcomes

#### JavaScript Migration
- ✅ Import statements converted to LaunchDarkly SDK
- ✅ `checkGate()` → `variation()` with false fallback
- ✅ `getConfig()` → `jsonVariation()` with complete fallback objects
- ⚠️ Experiments preserved with warning (not migrated)
- ✅ Session replay/autocapture → Observability plugins (if used)
- ✅ User context transformed to LDContext format

#### React Migration
- ✅ StatsigProvider → LDProvider (async or sync)
- ✅ `useGateValue()` → `useFlags().flagName`
- ✅ `useConfig()` → `useFlags().configName`
- ⚠️ `useExperiment()` preserved with warning
- ✅ `useStatsigClient()` → `useLDClient()`
- ✅ Plugins migrated to LaunchDarkly equivalents

#### TypeScript Migration
- ✅ Type imports updated for LaunchDarkly
- ✅ StatsigUser → LDContext with proper types
- ✅ Config interfaces maintained
- ✅ Generic types preserved where applicable
- ✅ Error handling patterns maintained
- ⚠️ Experiment types preserved with warnings

### Special Test Cases

#### 1. Experiments (Should NOT be migrated)
The test files include multiple experiments. The skill should:
- Detect experiments and warn they cannot be migrated
- Keep Statsig imports if experiments exist
- Mark related feature gates as blocked
- Generate report of what wasn't migrated

#### 2. Naming Conventions
Test files include flags with various naming:
- `snake_case_flag` → `snake-case-flag` (converted)
- `kebab-case-flag` → `kebab-case-flag` (maintained)
- `camelCaseFlag` → `camelCaseFlag` (maintained)

#### 3. Observability Features
- If `@statsig/session-replay` is imported → Add SessionReplay plugin
- If `@statsig/web-analytics` is imported → Add Observability plugin
- If neither is used → No plugins added to LaunchDarkly

#### 4. Complex User Context
Test proper transformation of:
- `userID` → `key`
- `custom` object → flattened properties
- `privateAttributes` → `_meta.privateAttributes` array
- `customIDs` → flattened (with warning about bucketing)

### Adding New Test Cases

To add a new test case:

1. Create a new file with clear feature demonstration
2. Include comments explaining what's being tested
3. Use a variety of Statsig features
4. Document expected migration behavior
5. Add edge cases and error scenarios

### Manual Verification

After running the migration skill:

1. **Check the migration report** (migration-summary.json)
2. **Verify all imports** are correctly converted
3. **Confirm fallback values** are properly set
4. **Ensure experiments** are preserved if present
5. **Validate user context** transformation
6. **Test the migrated code** (if possible)

### Common Issues to Test

1. **Mixed imports** (CommonJS and ES6)
2. **Nested config values** with complex types
3. **Private attributes** handling
4. **Silent gates** (disableExposureLog)
5. **Manual exposure logging**
6. **Override functions** for testing
7. **Cleanup/shutdown** patterns
8. **Error handling** and fallbacks

## Notes

- These test files are intentionally comprehensive to stress-test the migration skill
- Not all patterns may be common in real applications
- The skill should handle partial migrations gracefully
- Experiments are intentionally included to test the blocking behavior
- Some edge cases may require manual intervention

## Contributing

To improve test coverage:
1. Add new edge cases you encounter
2. Include real-world migration scenarios
3. Document any migration skill bugs found
4. Submit PRs with additional test patterns