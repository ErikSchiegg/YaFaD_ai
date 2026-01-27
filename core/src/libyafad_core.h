#include <stdarg.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>

typedef enum Action {
  Keep = 0,
  Migrate = 1,
  Vaporize = 2,
} Action;

typedef struct DecayResult {
  double new_utility;
  enum Action action;
} DecayResult;

double calculate_cascade_size(double base_size, int32_t level);

double calculate_decay(double u_last, double lambda, double delta_t);

double calculate_ideal_capacity(double total_records, int32_t tier);

struct DecayResult calculate_decay_with_horizon(double current_utility,
                                                double lambda,
                                                double time_delta,
                                                double horizon_threshold);
