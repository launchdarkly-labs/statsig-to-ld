/**
 * Statsig TypeScript Implementation Test File
 * 
 * This file demonstrates ALL Statsig TypeScript features for testing the migration skill:
 * - Full type annotations
 * - Interface definitions
 * - Generic types with configs
 * - Type-safe event logging
 * - Async/await patterns
 * - Error handling with types
 */

import { 
  StatsigClient,
  StatsigUser,
  StatsigOptions,
  DynamicConfig,
  Layer,
  StatsigEvent,
  InitializationDetails,
  EvaluationDetails
} from '@statsig/js-client';
import { 
  runStatsigSessionReplay,
  StatsigSessionReplayOptions 
} from '@statsig/session-replay';
import { 
  runStatsigAutoCapture,
  StatsigAutoCaptureOptions,
  AutoCaptureEvent
} from '@statsig/web-analytics';

// ============================================
// 1. TYPE DEFINITIONS
// ============================================

// Custom user properties interface
interface CustomUserProperties {
  accountType: 'free' | 'premium' | 'enterprise';
  company: string;
  role: 'admin' | 'manager' | 'developer' | 'user';
  department: string;
  teamSize: number;
  subscriptionTier: 'basic' | 'professional' | 'enterprise';
  betaTester: boolean;
  features: string[];
  metadata: Record<string, unknown>;
}

// Custom IDs interface
interface CustomIDs {
  organizationID: string;
  workspaceID: string;
  tenantID: string;
  sessionID: string;
}

// Private attributes interface
interface PrivateUserAttributes {
  ssn: string;
  creditScore: number;
  internalRating: number;
  apiKey: string;
}

// Config type definitions
interface HomepageConfig {
  hero_title: string;
  hero_subtitle: string;
  show_promotion_banner: boolean;
  promotion_discount: number;
  featured_products: Array<{
    id: string;
    name: string;
    price: number;
  }>;
  layout_version: 'v1' | 'v2' | 'v3';
}

interface PricingTierConfig {
  tiers: {
    [key: string]: {
      price: number;
      features: string[];
      limits: {
        users: number;
        storage: number;
        api_calls: number;
      };
    };
  };
  currency: 'USD' | 'EUR' | 'GBP';
  discount_code: string | null;
  annual_discount: number;
}

interface ThemeConfig {
  primary_color: string;
  secondary_color: string;
  dark_mode_default: boolean;
  font_family: string;
  border_radius: number;
  spacing_unit: number;
}

// Experiment type definitions
interface CheckoutExperiment {
  layout: 'single_page' | 'multi_step' | 'accordion';
  show_progress_bar: boolean;
  express_checkout: boolean;
  payment_methods: Array<'card' | 'paypal' | 'apple_pay' | 'google_pay'>;
  auto_fill_enabled: boolean;
  validation_type: 'instant' | 'on_submit';
}

interface SearchExperiment {
  algorithm: 'elasticsearch' | 'algolia' | 'custom';
  fuzzy_matching: boolean;
  max_results: number;
  show_categories: boolean;
  autocomplete_delay_ms: number;
  min_query_length: number;
}

// Event metadata interfaces
interface PurchaseEventMetadata {
  product_id: string;
  product_name: string;
  price: number;
  currency: string;
  quantity: number;
  category: string;
  payment_method: string;
}

interface ErrorEventMetadata {
  error_type: 'validation' | 'network' | 'permission' | 'unknown';
  error_code: string;
  error_message: string;
  stack_trace?: string;
  user_action: string;
}

// ============================================
// 2. USER CONTEXT SETUP WITH TYPES
// ============================================
const statsigUser: StatsigUser = {
  userID: 'typescript-user-789',
  email: 'developer@techcorp.com',
  ip: '10.0.0.1',
  userAgent: navigator.userAgent,
  country: 'US',
  locale: 'en-US',
  appVersion: '4.0.0',
  custom: {
    accountType: 'enterprise',
    company: 'TechCorp Industries',
    role: 'developer',
    department: 'Engineering',
    teamSize: 100,
    subscriptionTier: 'enterprise',
    betaTester: true,
    features: ['advanced_analytics', 'api_access', 'custom_integrations'],
    metadata: {
      onboarding_date: '2024-01-15',
      last_login: new Date().toISOString(),
      preferred_language: 'TypeScript'
    }
  } as CustomUserProperties,
  customIDs: {
    organizationID: 'org-ts-123',
    workspaceID: 'ws-ts-456',
    tenantID: 'tenant-ts-789',
    sessionID: `session-${Date.now()}`
  } as CustomIDs,
  privateAttributes: {
    ssn: '***-**-5678',
    creditScore: 750,
    internalRating: 4.8,
    apiKey: 'secret-key-xyz'
  } as PrivateUserAttributes
};

// ============================================
// 3. STATSIG CLIENT INITIALIZATION
// ============================================
const statsigOptions: StatsigOptions = {
  environment: {
    tier: 'production' as const
  },
  loggingIntervalMs: 5000,
  disableCurrentPageLogging: false,
  disableErrorLogging: false,
  disableAutoMetricsLogging: false,
  overrideStableID: undefined,
  localMode: false,
  initTimeoutMs: 3000,
  eventLoggingAPI: 'https://events.statsig.com/v1/log_event',
  disableLocalStorage: false,
  ignoreWindowUndefined: false
};

const statsigClient: StatsigClient = new StatsigClient(
  'client-typescript-sdk-key',
  statsigUser,
  statsigOptions
);

// Session replay options
const sessionReplayOptions: StatsigSessionReplayOptions = {
  maxSessionDurationMs: 1800000,
  recordConsoleErrors: true,
  privacyMask: true,
  maskAllInputs: false,
  maskTextContent: false,
  inlineStylesheet: true,
  sampling: {
    rate: 1.0,
    target: 'session'
  }
};

// Autocapture options
const autoCaptureOptions: StatsigAutoCaptureOptions = {
  eventFilterFunc: (event: AutoCaptureEvent): boolean => {
    return !event.eventName.startsWith('debug_');
  },
  captureClicks: true,
  captureScrolls: true,
  capturePageViews: true,
  captureFormSubmits: true,
  captureErrors: true,
  capturePerformance: true
};

// ============================================
// 4. ASYNC INITIALIZATION
// ============================================
async function initializeStatsig(): Promise<InitializationDetails> {
  try {
    // Initialize plugins before client
    runStatsigSessionReplay(statsigClient, sessionReplayOptions);
    runStatsigAutoCapture(statsigClient, autoCaptureOptions);

    // Initialize client
    const initDetails: InitializationDetails = await statsigClient.initializeAsync();
    
    console.log('Initialization details:', {
      duration: initDetails.duration,
      success: initDetails.success,
      source: initDetails.source,
      user: initDetails.user
    });

    return initDetails;
  } catch (error) {
    console.error('Failed to initialize Statsig:', error);
    throw error;
  }
}

// ============================================
// 5. FEATURE GATES WITH TYPES
// ============================================
class FeatureGateManager {
  private client: StatsigClient;

  constructor(client: StatsigClient) {
    this.client = client;
  }

  checkGates(): void {
    // Type-safe gate checking
    const newDashboard: boolean = this.client.checkGate('new_dashboard_design');
    const premiumFeatures: boolean = this.client.checkGate('premium_features_enabled');
    const betaAccess: boolean = this.client.checkGate('beta_program_access');
    const apiV3: boolean = this.client.checkGate('api_v3_enabled');
    
    // Gates with special naming
    const hyphenatedGate: boolean = this.client.checkGate('feature-with-hyphens');
    const underscoreGate: boolean = this.client.checkGate('feature_with_underscores');

    // Gate with evaluation details
    const gateWithDetails: EvaluationDetails = this.client.getFeatureGateWithDetails('detailed_gate');
    console.log('Gate evaluation reason:', gateWithDetails.reason);

    // Silent gate check
    const silentGate: boolean = this.client.checkGate('silent_feature', {
      disableExposureLog: true
    });

    // Process gates
    this.processGateResults({
      newDashboard,
      premiumFeatures,
      betaAccess,
      apiV3,
      hyphenatedGate,
      underscoreGate,
      silentGate
    });
  }

  private processGateResults(gates: Record<string, boolean>): void {
    Object.entries(gates).forEach(([gateName, isEnabled]) => {
      console.log(`Gate ${gateName}: ${isEnabled ? 'enabled' : 'disabled'}`);
    });
  }

  // Manual exposure logging
  logGateExposure(gateName: string): void {
    this.client.manuallyLogGateExposure(gateName);
  }
}

// ============================================
// 6. DYNAMIC CONFIGS WITH TYPES
// ============================================
class ConfigurationManager {
  private client: StatsigClient;

  constructor(client: StatsigClient) {
    this.client = client;
  }

  getTypedConfigs(): void {
    // Homepage configuration with type
    const homepageConfig: DynamicConfig = this.client.getConfig('homepage_configuration');
    const homepage: HomepageConfig = {
      hero_title: homepageConfig.get('hero_title', 'Welcome'),
      hero_subtitle: homepageConfig.get('hero_subtitle', 'Get started'),
      show_promotion_banner: homepageConfig.get('show_promotion_banner', false),
      promotion_discount: homepageConfig.get('promotion_discount', 0),
      featured_products: homepageConfig.get('featured_products', []),
      layout_version: homepageConfig.get('layout_version', 'v1')
    };

    // Pricing configuration with type
    const pricingConfig: DynamicConfig = this.client.getConfig('pricing_tiers');
    const pricing: PricingTierConfig = {
      tiers: pricingConfig.get('tiers', {}),
      currency: pricingConfig.get('currency', 'USD'),
      discount_code: pricingConfig.get('discount_code', null),
      annual_discount: pricingConfig.get('annual_discount', 0.1)
    };

    // Theme configuration with type
    const themeConfig: DynamicConfig = this.client.getConfig('ui_theme_config');
    const theme: ThemeConfig = {
      primary_color: themeConfig.get('primary_color', '#007bff'),
      secondary_color: themeConfig.get('secondary_color', '#6c757d'),
      dark_mode_default: themeConfig.get('dark_mode_default', false),
      font_family: themeConfig.get('font_family', 'Inter, sans-serif'),
      border_radius: themeConfig.get('border_radius', 4),
      spacing_unit: themeConfig.get('spacing_unit', 8)
    };

    // Config with evaluation details
    const configWithDetails = this.client.getConfigWithDetails('detailed_config');
    console.log('Config evaluation:', {
      reason: configWithDetails.details.reason,
      time: configWithDetails.details.time
    });

    this.applyConfigurations({ homepage, pricing, theme });
  }

  private applyConfigurations(configs: {
    homepage: HomepageConfig;
    pricing: PricingTierConfig;
    theme: ThemeConfig;
  }): void {
    console.log('Applying configurations:', configs);
  }

  // Manual config exposure
  logConfigExposure(configName: string): void {
    this.client.manuallyLogConfigExposure(configName);
  }
}

// ============================================
// 7. EXPERIMENTS WITH TYPES
// ============================================
class ExperimentManager {
  private client: StatsigClient;

  constructor(client: StatsigClient) {
    this.client = client;
  }

  runTypedExperiments(): void {
    // Checkout experiment with type
    const checkoutExp = this.client.getExperiment('checkout_flow_optimization');
    const checkoutConfig: CheckoutExperiment = {
      layout: checkoutExp.get('layout', 'single_page'),
      show_progress_bar: checkoutExp.get('show_progress_bar', true),
      express_checkout: checkoutExp.get('express_checkout', false),
      payment_methods: checkoutExp.get('payment_methods', ['card']),
      auto_fill_enabled: checkoutExp.get('auto_fill_enabled', true),
      validation_type: checkoutExp.get('validation_type', 'instant')
    };

    // Search experiment with type
    const searchExp = this.client.getExperiment('search_algorithm_test');
    const searchConfig: SearchExperiment = {
      algorithm: searchExp.get('algorithm', 'elasticsearch'),
      fuzzy_matching: searchExp.get('fuzzy_matching', true),
      max_results: searchExp.get('max_results', 20),
      show_categories: searchExp.get('show_categories', true),
      autocomplete_delay_ms: searchExp.get('autocomplete_delay_ms', 300),
      min_query_length: searchExp.get('min_query_length', 2)
    };

    // Layer experiment
    const uiLayer: Layer = this.client.getLayer('ui_experiments_layer');
    const buttonVariant: string = uiLayer.get('button_variant', 'primary');
    const animationSpeed: number = uiLayer.get('animation_speed_ms', 300);

    // Experiment with details
    const expWithDetails = this.client.getExperimentWithDetails('detailed_experiment');
    console.log('Experiment group:', expWithDetails.groupName);

    this.applyExperiments({ checkoutConfig, searchConfig, buttonVariant, animationSpeed });
  }

  private applyExperiments(experiments: {
    checkoutConfig: CheckoutExperiment;
    searchConfig: SearchExperiment;
    buttonVariant: string;
    animationSpeed: number;
  }): void {
    console.log('Applying experiments:', experiments);
  }

  // Manual experiment exposure
  logExperimentExposure(experimentName: string): void {
    this.client.manuallyLogExperimentExposure(experimentName);
  }
}

// ============================================
// 8. EVENT LOGGING WITH TYPES
// ============================================
class EventLogger {
  private client: StatsigClient;

  constructor(client: StatsigClient) {
    this.client = client;
  }

  logPurchaseEvent(metadata: PurchaseEventMetadata): void {
    this.client.logEvent('purchase_completed', metadata.price, metadata);
  }

  logErrorEvent(metadata: ErrorEventMetadata): void {
    this.client.logEvent('error_occurred', null, metadata);
  }

  logCustomEvent<T extends Record<string, unknown>>(
    eventName: string,
    value: string | number | null,
    metadata: T
  ): void {
    this.client.logEvent(eventName, value, metadata);
  }

  // Batch event logging
  async logBatchEvents(): Promise<void> {
    const events: StatsigEvent[] = [
      {
        eventName: 'page_view',
        value: null,
        metadata: { page: 'homepage', referrer: document.referrer }
      },
      {
        eventName: 'feature_interaction',
        value: null,
        metadata: { feature: 'search', action: 'query_submitted' }
      },
      {
        eventName: 'performance_metric',
        value: 234,
        metadata: { metric: 'api_latency_ms', endpoint: '/api/data' }
      }
    ];

    // Note: Individual event logging, batch API depends on implementation
    for (const event of events) {
      this.client.logEvent(event.eventName, event.value, event.metadata);
    }
  }
}

// ============================================
// 9. USER MANAGEMENT WITH TYPES
// ============================================
class UserManager {
  private client: StatsigClient;

  constructor(client: StatsigClient) {
    this.client = client;
  }

  async updateUser(updates: Partial<CustomUserProperties>): Promise<void> {
    const currentUser = this.client.getCurrentUser();
    
    const updatedUser: StatsigUser = {
      ...currentUser,
      custom: {
        ...(currentUser.custom as CustomUserProperties),
        ...updates,
        lastUpdated: new Date().toISOString()
      }
    };

    await this.client.updateUserAsync(updatedUser);
    
    this.client.logEvent('user_profile_updated', null, {
      updated_fields: Object.keys(updates),
      timestamp: Date.now()
    });
  }

  getCurrentUserInfo(): StatsigUser | null {
    return this.client.getCurrentUser();
  }

  getStableID(): string {
    return this.client.getStableID();
  }
}

// ============================================
// 10. ERROR HANDLING WITH TYPES
// ============================================
class StatsigErrorHandler {
  handleInitializationError(error: Error): void {
    console.error('Statsig initialization failed:', {
      message: error.message,
      stack: error.stack,
      timestamp: new Date().toISOString()
    });

    // Fallback behavior
    this.applyDefaultConfiguration();
  }

  handleEventLoggingError(error: Error, event: StatsigEvent): void {
    console.error('Failed to log event:', {
      event: event.eventName,
      error: error.message
    });

    // Retry logic or queue for later
    this.queueEventForRetry(event);
  }

  private applyDefaultConfiguration(): void {
    console.log('Applying default configuration due to initialization failure');
  }

  private queueEventForRetry(event: StatsigEvent): void {
    // Implementation for queuing events
    console.log('Queued event for retry:', event.eventName);
  }
}

// ============================================
// 11. MAIN APPLICATION CLASS
// ============================================
class StatsigApplication {
  private client: StatsigClient;
  private gateManager: FeatureGateManager;
  private configManager: ConfigurationManager;
  private experimentManager: ExperimentManager;
  private eventLogger: EventLogger;
  private userManager: UserManager;
  private errorHandler: StatsigErrorHandler;

  constructor() {
    this.client = statsigClient;
    this.gateManager = new FeatureGateManager(this.client);
    this.configManager = new ConfigurationManager(this.client);
    this.experimentManager = new ExperimentManager(this.client);
    this.eventLogger = new EventLogger(this.client);
    this.userManager = new UserManager(this.client);
    this.errorHandler = new StatsigErrorHandler();
  }

  async initialize(): Promise<void> {
    try {
      const initDetails = await initializeStatsig();
      console.log('App initialized with Statsig:', initDetails);
    } catch (error) {
      this.errorHandler.handleInitializationError(error as Error);
    }
  }

  async run(): Promise<void> {
    // Initialize
    await this.initialize();

    // Check feature gates
    this.gateManager.checkGates();

    // Get configurations
    this.configManager.getTypedConfigs();

    // Run experiments
    this.experimentManager.runTypedExperiments();

    // Log events
    await this.eventLogger.logBatchEvents();
    
    this.eventLogger.logPurchaseEvent({
      product_id: 'prod-123',
      product_name: 'Premium Subscription',
      price: 99.99,
      currency: 'USD',
      quantity: 1,
      category: 'subscription',
      payment_method: 'credit_card'
    });

    // Update user
    await this.userManager.updateUser({
      teamSize: 150,
      subscriptionTier: 'enterprise'
    });

    // Get evaluation details
    this.logEvaluationDetails();

    // Override for testing
    this.applyOverrides();

    // Cleanup on exit
    this.setupCleanup();
  }

  private logEvaluationDetails(): void {
    const allGates = this.client.getAllGates();
    const allConfigs = this.client.getAllConfigs();
    const allLayers = this.client.getAllLayers();

    console.log('Evaluation summary:', {
      gates: Object.keys(allGates).length,
      configs: Object.keys(allConfigs).length,
      layers: Object.keys(allLayers).length
    });
  }

  private applyOverrides(): void {
    // Override gate for testing
    this.client.overrideGate('test_gate_override', true);

    // Override config for testing
    this.client.overrideConfig('test_config_override', {
      test_value: 'overridden'
    });

    // Override layer for testing
    this.client.overrideLayer('test_layer_override', {
      variant: 'test'
    });
  }

  private setupCleanup(): void {
    // Cleanup on window unload
    if (typeof window !== 'undefined') {
      window.addEventListener('beforeunload', () => {
        this.client.logEvent('session_end', null, {
          session_duration_ms: Date.now() - performance.timeOrigin
        });
        this.client.shutdown();
      });
    }

    // Cleanup on process exit (Node.js)
    if (typeof process !== 'undefined') {
      process.on('exit', () => {
        this.client.shutdown();
      });
    }
  }
}

// ============================================
// 12. EXECUTE APPLICATION
// ============================================
const app = new StatsigApplication();

// Run the application
app.run().catch((error: Error) => {
  console.error('Application failed:', error);
  process.exit(1);
});

// ============================================
// 13. EXPORT FOR TESTING
// ============================================
export {
  StatsigApplication,
  FeatureGateManager,
  ConfigurationManager,
  ExperimentManager,
  EventLogger,
  UserManager,
  StatsigErrorHandler,
  statsigUser,
  statsigClient
};

// Type exports
export type {
  CustomUserProperties,
  CustomIDs,
  PrivateUserAttributes,
  HomepageConfig,
  PricingTierConfig,
  ThemeConfig,
  CheckoutExperiment,
  SearchExperiment,
  PurchaseEventMetadata,
  ErrorEventMetadata
};